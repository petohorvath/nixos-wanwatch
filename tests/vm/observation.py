"""WAN observations for the NixOS VM scenarios (PLAN §5.5 and §9.4).

State proves transitions, Prometheus proves live Probe progress, and kernel
routes independently prove Apply. The concrete NixOS machine supplies execute;
topology, fault injection, and scenario-specific assertions stay in the caller.
This file is prepended to testScript so the driver also checks it in each VM.
"""

import json as _json
import math as _math
import shlex as _shlex
import time as _time


class _Unavailable(Exception):
    """An observation command failed, for example before bootstrap completed."""


class Observation:
    def __init__(self, machine, curl="curl"):
        self._machine = machine
        self._scrape_command = (
            f"{_shlex.quote(curl)} --fail --silent --show-error --max-time 5 "
            "--unix-socket /run/wanwatch/metrics.sock http://wanwatch/metrics"
        )

    def _command(self, command, deadline):
        remaining = deadline - _time.monotonic()
        if remaining <= 0:
            raise _Unavailable("observation deadline expired")
        # The driver's timeout is in whole seconds. Account for command time in
        # the enclosing deadline too; a late successful read cannot pass a wait.
        status, output = self._machine.execute(
            command, timeout=_math.ceil(min(5, remaining))
        )
        if status != 0:
            raise _Unavailable(f"{command} exited {status}: {output}")
        return output

    def _diagnostics(self):
        sections = []
        for title, command in (
            ("state.json", "cat /run/wanwatch/state.json"),
            ("metrics", self._scrape_command),
            ("ip -4 route show table all", "ip -4 route show table all"),
            ("ip -6 route show table all", "ip -6 route show table all"),
            (
                "last 50 wanwatch.service log lines",
                "journalctl -u wanwatch.service --no-pager -n 50 -o cat",
            ),
        ):
            # Failure evidence has a separate, bounded budget. A broken source
            # must not hide the others or replace the original timeout message.
            try:
                output = self._command(command, _time.monotonic() + 2)
            except Exception as error:
                output = f"<unavailable: {error}>"
            sections.append(f"===== {title} =====\n{output}")
        return "\n".join(sections)

    def _wait(self, description, read, matches, timeout):
        if not _math.isfinite(timeout) or timeout <= 0:
            raise ValueError("timeout must be positive and finite")
        deadline = _time.monotonic() + timeout
        last = None
        while _time.monotonic() < deadline:
            try:
                last = read(deadline)
                if matches(last) and _time.monotonic() <= deadline:
                    return last
            except _Unavailable as error:
                last = str(error)
            remaining = deadline - _time.monotonic()
            if remaining > 0:
                _time.sleep(min(0.1, remaining))
        raise AssertionError(
            f"Timed out after {timeout}s waiting for {description}; "
            f"last observation: {last!r}\n{self._diagnostics()}"
        )

    @staticmethod
    def _contains(actual, expected):
        if isinstance(expected, dict):
            return isinstance(actual, dict) and all(
                key in actual and Observation._contains(actual[key], value)
                for key, value in expected.items()
            )
        return type(actual) is type(expected) and actual == expected

    def state(self, expected=None, timeout=15):
        """Return one schema-1 snapshot matching all expected fields together.

        Nested dictionaries are partial matches; a missing field never matches
        null or false. Use this for compound State assertions and transition
        snapshots, never to wait for later per-Sample statistics.
        """
        want = {"schema": 1, **(expected or {})}
        return self._wait(
            f"State containing {want!r}",
            lambda deadline: _json.loads(
                self._command("cat /run/wanwatch/state.json", deadline)
            ),
            lambda snapshot: self._contains(snapshot, want),
            timeout,
        )

    def wait_active(self, group, active, timeout=15):
        """Selection can succeed on cold-start carrier Health alone."""
        return self.state({"groups": {group: {"active": active}}}, timeout)

    def wait_healthy(self, wan, timeout=15):
        """Wait for aggregate Probe Health, including an unselected backup.

        Gate on this before injecting loss: carrier-only Selection does not
        prove either WAN's Probe Window has cooked.
        """
        return self.state({"wans": {wan: {"healthy": True}}}, timeout)

    @staticmethod
    def _family_flag(family):
        return {"v4": "-4", "v6": "-6"}[family]

    def wait_gateway(self, wan, family, gateway, timeout=15):
        self._family_flag(family)
        return self.state({"wans": {wan: {"gateways": {family: gateway}}}}, timeout)

    def scrape(self, timeout=10):
        """Read the live endpoint; a failed scrape is never an empty scrape."""
        return self._wait(
            "Prometheus scrape",
            lambda deadline: self._command(self._scrape_command, deadline),
            lambda body: True,
            timeout,
        )

    @staticmethod
    def _series(name, labels):
        # client_golang emits label pairs in alphabetical order. Keep knowledge
        # of the wire format here, including quoting and absent Vec series.
        return (
            name
            + "{"
            + ",".join(
                f"{key}={_json.dumps(value)}" for key, value in sorted(labels.items())
            )
            + "}"
        )

    @staticmethod
    def _metric(body, series):
        prefix = series + " "
        for line in body.splitlines():
            if line.startswith(prefix):
                value = float(line[len(prefix) :].split()[0])
                if not _math.isfinite(value):
                    raise ValueError(f"non-finite metric {series}: {value}")
                return value
        return None

    def decisions(self, group, reason):
        """An unobserved Decision counter is zero; gauges never use this rule."""
        series = self._series(
            "wanwatch_group_decisions_total", {"group": group, "reason": reason}
        )
        value = self._metric(self.scrape(), series)
        return 0.0 if value is None else value

    def _wait_metrics(self, description, matches, timeout):
        return self._wait(
            description,
            lambda deadline: self._command(self._scrape_command, deadline),
            matches,
            timeout,
        )

    def wait_decisions(self, group, reason, minimum, timeout=10):
        series = self._series(
            "wanwatch_group_decisions_total", {"group": group, "reason": reason}
        )
        return self._wait_metrics(
            f"{series} >= {minimum}",
            lambda body: (
                (value := self._metric(body, series)) is not None and value >= minimum
            ),
            timeout,
        )

    def wait_probe_loss(self, wan, family, low, high=1.0, timeout=10):
        """Wait for a present live gauge in the inclusive range [low, high]."""
        self._family_flag(family)
        if not 0 <= low <= high <= 1:
            raise ValueError("Probe loss bounds must satisfy 0 <= low <= high <= 1")
        series = self._series(
            "wanwatch_probe_loss_ratio", {"wan": wan, "family": family}
        )
        body = self._wait_metrics(
            f"live {series} in [{low}, {high}]",
            lambda body: (
                (value := self._metric(body, series)) is not None
                and low <= value <= high
            ),
            timeout,
        )
        return self._metric(body, series)

    def wait_family_metrics(self, wan, families, timeout=10):
        """Wait for all per-family Health gauges in the same live scrape."""
        for family in families:
            self._family_flag(family)
        series = {
            self._series(
                "wanwatch_wan_family_healthy", {"wan": wan, "family": family}
            ): float(healthy)
            for family, healthy in families.items()
        }
        return self._wait_metrics(
            f"family Health gauges {series!r}",
            lambda body: all(
                self._metric(body, name) == want for name, want in series.items()
            ),
            timeout,
        )

    def wait_default_route(
        self, family, interface, *, group=None, gateway=None, timeout=10
    ):
        """Verify Apply in the kernel, independently of State.

        group=None reads the main table (Gateway-discovery precondition).
        Otherwise resolve the Group's table from config. gateway=None requires
        a direct default route with no next-hop, as used by pointToPoint WANs.
        """
        flag = self._family_flag(family)

        def read(deadline):
            table = "main"
            if group is not None:
                config = _json.loads(
                    self._command("cat /etc/wanwatch/config.json", deadline)
                )
                table = str(int(config["groups"][group]["table"]))
            return _json.loads(
                self._command(
                    f"ip -j {flag} route show table {table} default", deadline
                )
            )

        return self._wait(
            f"{family} default route in {group or 'main'!r} via {gateway!r} dev {interface}",
            read,
            lambda routes: any(
                route.get("dst") == "default"
                and route.get("dev") == interface
                and route.get("gateway") == gateway
                and route.get("type", "unicast") == "unicast"
                for route in routes
            ),
            timeout,
        )
