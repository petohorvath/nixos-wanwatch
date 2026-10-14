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
  inherit (helpers) getErrorKinds;
  inherit (wanwatch) member;

  memberFieldTests = helpers.fieldTests {
    inherit (member) tryMake;
    input = minimal;
  };
in
{
  defaults = {
    testValues = {
      expr = member.defaults;
      expected = {
        priority = 1;
      };
    };

    testFillMinimalInput = {
      expr = member.make minimal;
      expected = member.defaults // minimal;
    };
  };

  fields = {
    wan = memberFieldTests "wan" "memberInvalidWan" cases.identifiers;
    priority = memberFieldTests "priority" "memberInvalidPriority" cases.positiveInts;
  };

  rejections = helpers.rejectionTests member.tryMake {
    memberInvalidWan.missingWan = { };
    # Member has no `weight` until a Strategy reads one.
    memberUnknownField.weight = minimal // {
      weight = 100;
    };
  };

  # WANs, Groups, and Members report invalid identifiers alike.
  testInvalidWanMessage = {
    expr = (member.tryMake (minimal // { wan = "1bad"; })).error;
    expected = "member.make: [memberInvalidWan] wan must be a valid wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*); got \"1bad\"";
  };

  testReportsEveryViolation = {
    expr = getErrorKinds (
      member.tryMake {
        wan = "1bad";
        priority = -1;
      }
    );
    expected = [
      "memberInvalidWan"
      "memberInvalidPriority"
    ];
  };
}
