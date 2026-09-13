# VM observations

Scenarios prepend `observation.py` to their `testScript` and construct
`Observation(router, curl="${pkgs.curl}/bin/curl")`. This keeps the shared code
inside the NixOS driver's Python lint and type checks on both channels.
The observer accepts the existing machine directly. Topology, network readiness,
fault injection, Hook assertions, and Telegraf output checks stay in scenarios.

Choose the observation that proves the behavior (PLAN §5.5):

- `wait_active(group, active)` reads Selection from State. `active=None`
  requires an explicit JSON null. Cold-start Selection can use carrier alone.
- `wait_healthy(wan)` reads aggregate Probe Health from State. Use it to gate
  fault injection until both primary and backup have cooked Probe Windows.
- `wait_gateway(wan, family, gateway)` reads the discovered Gateway from State.
- `state(expected)` returns one schema-1 snapshot containing every field in a
  nested partial dictionary. Use it for compound assertions that must hold
  together. `state()` reads a snapshot for scenario-specific assertions.
- `wait_probe_loss(wan, family, low, high=1.0)` reads live Prometheus statistics,
  with inclusive bounds. State freezes those statistics between transitions.
- `wait_family_metrics(wan, families)` checks all specified Health gauges in
  one live scrape, for example `{"v4": True, "v6": False}`.
- `decisions(group, reason)` reads a counter; `wait_decisions(group, reason,
  minimum)` waits for it to appear and reach a minimum. Only an unobserved
  Decision counter reads as zero. Missing gauges never satisfy zero or false.
- `wait_default_route(family, interface, group=..., gateway=...)` checks the
  kernel via `ip -j`, independently of State. It resolves the Group's table
  from config. Omit `group` to check the main table; omit `gateway` to require
  a direct default route with no next-hop.
- `scrape()` returns the live endpoint for scenario-specific metric assertions.

Waits use monotonic deadlines that include command time and host-side sleeps.
Each command is limited to the remaining budget, rounded up to the driver's
whole-second timeout, with a five-second cap. A read completing after the
deadline cannot pass. On timeout, the assertion includes the last observation,
State, metrics, both families' routes, and the last 50 daemon journal lines.
Diagnostic reads have a separate two-second budget per source; a failed source
does not suppress the rest. Nonzero command exits retry during startup or
convergence; malformed JSON or metrics fail immediately.

Run the observer's contract tests without KVM:

```sh
python3 -B -m unittest discover -s tests/vm -p test_observation.py -v
nix build .#checks.x86_64-linux.observation
```

These checks also run in CI and `nix flake check`. The real `vm-*` and
`vm-unstable-*` scenarios remain the end-to-end checks for the daemon and kernel.
