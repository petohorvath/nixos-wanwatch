/*
  Tests for `lib/types/group.nix`: only what the type system enforces.
  `skeleton.nix` checks that the `group` submodule evaluates inputs to
  what `group.make` builds; `internal/group.nix` covers cross-field
  checks such as non-empty members and duplicate WAN references.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (helpers) typeTests;
  inherit (wanwatch) types;

  # `groups.<name>` supplies the name, so inputs leave it out.
  minimal = removeAttrs fixtures.inputs.group.minimal [ "name" ];
in
{
  groupName = typeTests types.groupName cases.identifiers;
  groupStrategy = typeTests types.groupStrategy cases.strategies;
  groupTable = typeTests types.groupTable cases.markTableIds;
  groupMark = typeTests types.groupMark cases.markTableIds;

  # `mark` and `table` have no defaults, so a Group must declare both.
  group = typeTests types.group {
    invalid = [
      (removeAttrs minimal [ "mark" ])
      (removeAttrs minimal [ "table" ])
      (minimal // { members = [ { wan = "1bad"; } ]; })
      (minimal // { strategy = "magic"; })
      (minimal // { mark = 0; })
      (minimal // { table = 0; })
    ];
  };
}
