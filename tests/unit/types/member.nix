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
  inherit (helpers) evalSubmodule typeTests;
  inherit (wanwatch) types;

  # `members.<wan>` supplies the WAN name, so inputs leave it out.
  minimal = removeAttrs fixtures.inputs.member.minimal [ "wan" ];
in
{
  memberWan = typeTests types.memberWan cases.identifiers;
  memberPriority = typeTests types.memberPriority cases.positiveInts;

  member = {
    testWanTakesAttributeKey = {
      expr = (evalSubmodule "wan" types.member (minimal // { wan = "backup"; })).wan;
      expected = "backup";
    };
  }
  // typeTests types.member {
    invalid = [
      # The attribute key sets the read-only `wan`.
      (minimal // { wan = "backup"; })
      (minimal // { priority = 0; })
      # Member has no `weight` until a Strategy reads one.
      (minimal // { weight = 100; })
    ];
  };
}
