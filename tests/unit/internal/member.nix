/*
  Tests for `lib/internal/member.nix`, exposed as `wanwatch.member`.
  `skeleton.nix` covers the `make` / `tryMake` / `toJSONValue`
  contract.
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
  inherit (helpers) fieldTests getErrorKinds;
  inherit (wanwatch) member;

  memberField =
    field: kind:
    fieldTests {
      inherit (member) tryMake;
      inherit kind;
      input = minimal;
      path = [ field ];
    };
in
{
  defaults = {
    testValues = {
      expr = member.defaults;
      expected = {
        weight = 100;
        priority = 1;
      };
    };

    testFillMinimalInput = {
      expr = member.make minimal;
      expected = member.defaults // minimal;
    };
  };

  fields = {
    wan = memberField "wan" "memberInvalidWan" cases.identifiers;
    weight = memberField "weight" "memberInvalidWeight" cases.positiveInts;
    priority = memberField "priority" "memberInvalidPriority" cases.positiveInts;
  };

  rejections = helpers.rejectionTests member.tryMake {
    memberInvalidWan.missingWan = { };
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      member.tryMake {
        wan = "1bad";
        weight = 0;
        priority = -1;
      }
    );
    expected = [
      "memberInvalidWan"
      "memberInvalidWeight"
      "memberInvalidPriority"
    ];
  };
}
