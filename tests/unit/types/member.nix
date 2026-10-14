/*
  Tests for `lib/types/member.nix`. `skeleton.nix` checks that the
  `member` submodule evaluates inputs to what `member.make` builds.
*/
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (fixtures.inputs.member) minimal;
  inherit (helpers) typeTests;
  inherit (wanwatch) types;
in
{
  memberWan = typeTests types.memberWan cases.identifiers;
  memberPriority = typeTests types.memberPriority cases.positiveInts;

  member = typeTests types.member {
    invalid = [
      { }
      (minimal // { wan = "1bad"; })
      # Member has no `weight` until a Strategy reads one.
      (minimal // { weight = 100; })
      (minimal // { priority = 0; })
    ];
  };
}
