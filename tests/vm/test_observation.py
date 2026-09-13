"""Observation contracts, exercised without KVM through the scenario interface."""

import json
import unittest
from collections import deque
from unittest.mock import patch

from observation import Observation


STATE = "cat /run/wanwatch/state.json"
CONFIG = "cat /etc/wanwatch/config.json"
SCRAPE = (
    "curl --fail --silent --show-error --max-time 5 "
    "--unix-socket /run/wanwatch/metrics.sock http://wanwatch/metrics"
)
JOURNAL = "journalctl -u wanwatch.service --no-pager -n 50 -o cat"


def snapshot(**fields):
    return json.dumps({"schema": 1, **fields})


class ScriptedMachine:
    """Local command results at the existing machine.execute seam."""

    def __init__(self, responses):
        self.responses = {
            command: deque(values) for command, values in responses.items()
        }
        self.calls = []

    def execute(self, command, timeout):
        self.calls.append((command, timeout))
        values = self.responses[command]
        value = values.popleft() if len(values) > 1 else values[0]
        if isinstance(value, Exception):
            raise value
        return value if isinstance(value, tuple) else (0, value)

    def count(self, command):
        return sum(cmd == command for cmd, _ in self.calls)


class ObservationTests(unittest.TestCase):
    def setUp(self):
        self.now = 0.0
        monotonic = patch("observation._time.monotonic", side_effect=lambda: self.now)
        sleep = patch("observation._time.sleep", side_effect=self.advance)
        monotonic.start()
        sleep.start()
        self.addCleanup(monotonic.stop)
        self.addCleanup(sleep.stop)

    def advance(self, seconds):
        self.now += seconds

    def make_observation(self, responses):
        sources = {
            STATE: [snapshot()],
            CONFIG: [json.dumps({"groups": {"home": {"table": 2042}}})],
            SCRAPE: ["# no observations yet\n"],
            "ip -4 route show table all": ["v4 kernel evidence"],
            "ip -6 route show table all": ["v6 kernel evidence"],
            JOURNAL: ["daemon journal evidence"],
            **responses,
        }
        machine = ScriptedMachine(sources)
        return Observation(machine), machine

    def test_selection_waits_through_bootstrap_and_missing_active(self):
        selected = snapshot(groups={"home": {"active": "primary"}})
        observe, machine = self.make_observation(
            {
                STATE: [
                    (1, "state.json does not exist"),
                    snapshot(schema=2, groups={"home": {"active": "primary"}}),
                    snapshot(groups={"home": {}}),
                    selected,
                ]
            }
        )
        self.assertEqual(observe.wait_active("home", "primary"), json.loads(selected))
        self.assertEqual(machine.count(STATE), 4)

    def test_null_selection_requires_a_present_field(self):
        observe, machine = self.make_observation(
            {
                STATE: [
                    snapshot(groups={"home": {}}),
                    snapshot(groups={"home": {"active": None}}),
                ]
            }
        )
        observe.wait_active("home", None)
        self.assertEqual(machine.count(STATE), 2)

    def test_probe_health_is_distinct_from_cold_start_selection(self):
        observe, machine = self.make_observation(
            {
                STATE: [
                    snapshot(
                        groups={"home": {"active": "primary"}},
                        wans={"backup": {"healthy": False}},
                    ),
                    snapshot(wans={"backup": {"healthy": True}}),
                ]
            }
        )
        observe.wait_healthy("backup")
        self.assertEqual(machine.count(STATE), 2)

    def test_compound_state_requires_every_field_in_one_snapshot(self):
        expected = {
            "wans": {
                "uplink": {
                    "healthy": False,
                    "families": {
                        "v4": {"healthy": True},
                        "v6": {"healthy": False},
                    },
                }
            },
            "groups": {"home": {"active": None}},
        }
        observe, machine = self.make_observation(
            {
                STATE: [
                    snapshot(wans=expected["wans"]),
                    snapshot(groups=expected["groups"]),
                    snapshot(**expected),
                ]
            }
        )
        self.assertEqual(observe.state(expected), {"schema": 1, **expected})
        self.assertEqual(machine.count(STATE), 3)

    def test_gateway_uses_state_for_each_family(self):
        for family, gateway in (("v4", "192.0.2.1"), ("v6", "fd00:1::1")):
            with self.subTest(family=family):
                observe, machine = self.make_observation(
                    {
                        STATE: [
                            snapshot(wans={"uplink": {"gateways": {family: ""}}}),
                            snapshot(wans={"uplink": {"gateways": {family: gateway}}}),
                        ]
                    }
                )
                observe.wait_gateway("uplink", family, gateway)
                self.assertEqual(machine.count(STATE), 2)

    def test_malformed_state_is_not_hidden_by_retry(self):
        observe, machine = self.make_observation({STATE: ["{broken"]})
        with self.assertRaises(json.JSONDecodeError):
            observe.wait_active("home", "primary")
        self.assertEqual(machine.count(STATE), 1)

    def test_zero_loss_requires_the_requested_live_series(self):
        for family in ("v4", "v6"):
            with self.subTest(family=family):
                series = f'wanwatch_probe_loss_ratio{{family="{family}",wan="primary"}}'
                observe, machine = self.make_observation(
                    {
                        STATE: [
                            snapshot(
                                wans={
                                    "primary": {
                                        "families": {
                                            family: {"lossRatio": 0.0},
                                        }
                                    }
                                }
                            )
                        ],
                        SCRAPE: [
                            "",
                            series.replace("primary", "backup") + " 0\n",
                            series + " 0.2\n",
                            series + " 0\n",
                        ],
                    }
                )
                self.assertEqual(observe.wait_probe_loss("primary", family, 0, 0.1), 0)
                self.assertEqual(machine.count(SCRAPE), 4)
                self.assertEqual(machine.count(STATE), 0)

    def test_loss_bounds_are_inclusive(self):
        series = 'wanwatch_probe_loss_ratio{family="v6",wan="primary"}'
        for value in (0.25, 0.75):
            with self.subTest(value=value):
                observe, _ = self.make_observation({SCRAPE: [f"{series} {value}\n"]})
                self.assertEqual(
                    observe.wait_probe_loss("primary", "v6", 0.25, 0.75), value
                )

    def test_scrape_failure_retries_without_becoming_zero(self):
        series = 'wanwatch_probe_loss_ratio{family="v4",wan="primary"}'
        observe, machine = self.make_observation(
            {
                SCRAPE: [
                    (7, "socket not ready"),
                    series + " 0\n",
                ]
            }
        )
        observe.wait_probe_loss("primary", "v4", 0, 0)
        self.assertEqual(machine.count(SCRAPE), 2)

    def test_non_finite_probe_metrics_fail_loudly(self):
        for value in ("NaN", "+Inf", "-Inf"):
            with self.subTest(value=value):
                observe, _ = self.make_observation(
                    {
                        SCRAPE: [
                            f'wanwatch_probe_loss_ratio{{family="v4",wan="primary"}} {value}\n',
                        ]
                    }
                )
                with self.assertRaisesRegex(ValueError, "non-finite metric"):
                    observe.wait_probe_loss("primary", "v4", 0)

    def test_absent_decision_counter_is_zero_but_present_counter_is_parsed(self):
        observe, _ = self.make_observation(
            {
                SCRAPE: [
                    "",
                    'wanwatch_group_decisions_total{group="home",reason="health"} 2e0\n',
                ]
            }
        )
        self.assertEqual(observe.decisions("home", "health"), 0)
        self.assertEqual(observe.decisions("home", "health"), 2)

    def test_decision_wait_requires_counter_to_reach_minimum(self):
        series = 'wanwatch_group_decisions_total{group="home",reason="carrier"}'
        observe, machine = self.make_observation(
            {
                SCRAPE: [
                    "",
                    series + " 1\n",
                    series + " 2\n",
                ]
            }
        )
        observe.wait_decisions("home", "carrier", minimum=2)
        self.assertEqual(machine.count(SCRAPE), 3)

    def test_family_gauges_must_be_present_together_even_when_false(self):
        v4 = 'wanwatch_wan_family_healthy{family="v4",wan="uplink"} 1\n'
        v6 = 'wanwatch_wan_family_healthy{family="v6",wan="uplink"} 0\n'
        observe, machine = self.make_observation({SCRAPE: [v4, v6, v4 + v6]})
        observe.wait_family_metrics("uplink", {"v4": True, "v6": False})
        self.assertEqual(machine.count(SCRAPE), 3)

    def test_default_route_checks_family_table_destination_interface_and_next_hop(self):
        for family, flag in (("v4", "-4"), ("v6", "-6")):
            with self.subTest(family=family):
                command = f"ip -j {flag} route show table 2042 default"
                expected = {"dst": "default", "dev": "wan0"}
                observe, machine = self.make_observation(
                    {
                        STATE: [snapshot(groups={"home": {"active": "primary"}})],
                        command: [
                            (2, "FIB table does not exist"),
                            json.dumps([{**expected, "dst": "192.0.2.0/24"}]),
                            json.dumps([{**expected, "dev": "wan01"}]),
                            json.dumps([{**expected, "gateway": "192.0.2.1"}]),
                            json.dumps([{**expected, "type": "unreachable"}]),
                            json.dumps([expected]),
                        ],
                    }
                )
                self.assertEqual(
                    observe.wait_default_route(family, "wan0", group="home"), [expected]
                )
                self.assertEqual(machine.count(command), 6)
                self.assertEqual(machine.count(STATE), 0)

    def test_main_and_group_default_routes_match_discovered_gateway(self):
        for family, flag, gateway in (
            ("v4", "-4", "192.0.2.1"),
            ("v6", "-6", "fd00:1::1"),
        ):
            for group, table in ((None, "main"), ("home", "2042")):
                with self.subTest(family=family, group=group):
                    command = f"ip -j {flag} route show table {table} default"
                    expected = {"dst": "default", "dev": "eth1", "gateway": gateway}
                    observe, machine = self.make_observation(
                        {
                            command: [
                                json.dumps([{**expected, "gateway": "wrong"}]),
                                json.dumps([expected]),
                            ]
                        }
                    )
                    observe.wait_default_route(
                        family, "eth1", group=group, gateway=gateway
                    )
                    self.assertEqual(machine.count(command), 2)

    def test_timeout_reports_missing_gauge_and_every_diagnostic_source(self):
        observe, _ = self.make_observation({})
        with self.assertRaises(AssertionError) as caught:
            observe.wait_probe_loss("primary", "v4", 0, 0, timeout=0.25)
        message = str(caught.exception)
        for expected in (
            "0.25s",
            "wanwatch_probe_loss_ratio",
            "last observation",
            "state.json",
            "metrics",
            "v4 kernel evidence",
            "v6 kernel evidence",
            "daemon journal evidence",
        ):
            self.assertIn(expected, message)
        self.assertAlmostEqual(self.now, 0.25)

    def test_slow_successful_read_cannot_outlive_deadline(self):
        observe, machine = self.make_observation(
            {
                STATE: [
                    snapshot(groups={"home": {"active": "primary"}}),
                ]
            }
        )
        execute = machine.execute

        def slow_execute(command, timeout):
            if not machine.calls:
                self.advance(2)
            return execute(command, timeout)

        machine.execute = slow_execute
        with self.assertRaisesRegex(AssertionError, "Timed out after 1s"):
            observe.wait_active("home", "primary", timeout=1)
        self.assertEqual(machine.calls[0][1], 1)

    def test_failed_diagnostic_does_not_hide_original_failure_or_other_sources(self):
        observe, machine = self.make_observation(
            {
                STATE: [(1, "state missing")],
                "ip -4 route show table all": [RuntimeError("v4 unavailable")],
            }
        )
        with self.assertRaises(AssertionError) as caught:
            observe.wait_active("home", "primary", timeout=0.1)
        message = str(caught.exception)
        for expected in (
            "state missing",
            "v4 unavailable",
            "v6 kernel evidence",
            "daemon journal evidence",
        ):
            self.assertIn(expected, message)
        self.assertTrue(all(0 < timeout <= 2 for _, timeout in machine.calls))

    def test_invalid_wait_arguments_fail_before_reading(self):
        observe, machine = self.make_observation({})
        for timeout in (0, -1, float("inf"), float("nan")):
            with self.subTest(timeout=timeout), self.assertRaises(ValueError):
                observe.state(timeout=timeout)
        for low, high in ((-0.1, 1), (0.5, 0.25), (0, 1.1)):
            with self.subTest(low=low, high=high), self.assertRaises(ValueError):
                observe.wait_probe_loss("primary", "v4", low, high)
        with self.assertRaises(KeyError):
            observe.wait_gateway("primary", "v5", "")
        self.assertEqual(machine.calls, [])


if __name__ == "__main__":
    unittest.main()
