/*
  Pure-Nix mirror of the daemon's selector (`daemon/internal/selector`),
  exposed as `wanwatch.selector`. Consumers and tests can predict the
  daemon's Selection without running it; `tests/unit/internal/selector.nix`
  mirrors the Go selector tests, and the two are compared by hand.
*/
{
  lib,
}:
let
  # Lowest priority among healthy Members wins; ties go to the
  # lexicographically smaller WAN name. Weight is ignored. Matches
  # `daemon/internal/selector/primarybackup.go`.
  primaryBackup =
    group: memberHealth:
    let
      isHealthy = member: memberHealth.${member.wan} or false;
      isPreferred =
        left: right:
        if left.priority != right.priority then left.priority < right.priority else left.wan < right.wan;
      healthyMembers = lib.sort isPreferred (builtins.filter isHealthy group.members);
    in
    if healthyMembers == [ ] then null else (builtins.head healthyMembers).wan;

  /*
    Strategy implementations by name, so tests can check that Nix and
    the daemon recognise the same Strategies.

    Each value takes a group value and the `memberHealth` attrset
    described for `compute`, and returns the selected WAN name or
    null.
  */
  strategies = {
    "primary-backup" = primaryBackup;
  };

  /*
    Compute a Group's Selection with the same rules as the daemon.

    `group`: a group value from `group.make`.
    `memberHealth`: an attrset mapping WAN names to their Health as a
    bool; missing WANs count as unhealthy.

    Returns `{ group = <group name>; active = <WAN name or null>; }`,
    where `active` is null when no Member is healthy. Throws for an
    unknown strategy, which `group.make` already rejects.
  */
  compute =
    group: memberHealth:
    let
      selectActive =
        strategies.${group.strategy}
          or (throw "wanwatch: selector.compute: unknown strategy ${builtins.toJSON group.strategy} for group ${builtins.toJSON group.name}");
    in
    {
      group = group.name;
      active = selectActive group memberHealth;
    };
in
{
  inherit compute strategies;
}
