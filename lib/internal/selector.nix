/*
  internal/selector — pure-Nix mirror of the Go selector.

  Exposed under `wanwatch.internal.selector` and (for convenience)
  `wanwatch.selector`. Implements the same decision logic the daemon
  runs at runtime (`daemon/internal/selector/`), so module-level
  consumers and tests can predict the daemon's selection without
  running the daemon.

  Two reasons to keep a Nix mirror:

    1. Module-level config validation — `lib/internal/config.nix`
       can dry-run the selector against a hypothetical "all up"
       Health to confirm the group's structural soundness produces
       *some* Selection (e.g. catches a group declared with one
       member that has an unreachable priority).
    2. Test parity — `tests/unit/internal/selector.nix` exercises
       the same scenarios as
       `daemon/internal/selector/primarybackup_test.go`. Drift
       between Nix and Go is caught by a manual match-up of test
       names + expected outputs.

  ===== compute =====

  `compute group memberHealth → { group; active; }`:
    - `group` (input): a wanwatch group value (`internal.group.make`
      result).
    - `memberHealth`: an attrset mapping wan-name → bool, the
      runtime Health verdict for each WAN.
    - Output: `{ group = "<group.name>"; active = <wan-name | null>; }`.
      `null` when no member is healthy.

  Throws if `group.strategy` is unknown — that should be impossible
  at this point (the group value-type validator already rejected
  it at construction).

  ===== strategies =====

  Attrset of `<strategy-name> = <fn>;`. Closed-set; v1 has one
  entry. The keyset is exposed for tests that want to assert
  "wanwatch and the daemon recognise the same strategies".

  ===== primary-backup =====

  Lowest priority among healthy members wins. Ties broken by
  lexicographic WAN name. Weight is ignored (matters once
  multi-active lands in v2). Matches `primaryBackup` in
  `daemon/internal/selector/primarybackup.go` byte for byte.
*/
{
  lib,
  libnet,
  internal,
}:
let
  inherit (internal) member;

  primaryBackup =
    group: memberHealth:
    let
      isHealthy = m: memberHealth.${m.wan} or false;
      healthy = builtins.filter isHealthy group.members;
      cmp = a: b: if a.priority != b.priority then a.priority < b.priority else a.wan < b.wan;
      sorted = lib.sort cmp healthy;
    in
    if sorted == [ ] then null else (builtins.head sorted).wan;

  strategies = {
    "primary-backup" = primaryBackup;
  };

  compute =
    group: memberHealth:
    let
      strategyFn =
        strategies.${group.strategy}
          or (builtins.throw "wanwatch: selector.compute: unknown strategy ${builtins.toJSON group.strategy} for group ${builtins.toJSON group.name}");
    in
    {
      group = group.name;
      active = strategyFn group memberHealth;
    };
in
{
  inherit compute strategies;
}
