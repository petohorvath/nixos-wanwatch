/*
  Skeleton tests: every value type (probe, member, wan, group)
  implements the common `make` / `tryMake` / `toJSONValue` contract,
  and its submodule option type in `wanwatch.types` evaluates an input
  to what `make` builds from it, defaults included. The per-type suites
  test what is specific to each type. Add each new value type to
  `invalidInputs`.

  Pure-function modules (selector, config) use purpose-specific APIs
  and are not checked here.
*/
{
  fixtures,
  helpers,
  lib,
  wanwatch,
  ...
}:
let
  inherit (helpers) evalSubmodule getErrorKinds;

  # One rejected input per value type and the single kind it reports.
  invalidInputs = {
    probe = {
      input.targets = { };
      kind = "probeNoTargets";
    };
    member = {
      input = { };
      kind = "memberInvalidWan";
    };
    wan = {
      input = fixtures.inputs.wan.minimal // {
        name = "";
      };
      kind = "wanInvalidName";
    };
    group = {
      input = fixtures.inputs.group.minimal // {
        members = [ ];
      };
      kind = "groupNoMembers";
    };
  };

  contractTests =
    typeName: invalid:
    let
      valueType = wanwatch.${typeName};
      inherit (fixtures.inputs.${typeName}) full minimal;
      serialize = input: valueType.toJSONValue (valueType.make input);
    in
    {
      testTryMakeSucceeds = {
        expr = removeAttrs (valueType.tryMake minimal) [ "value" ];
        expected = {
          success = true;
          error = null;
        };
      };

      testTryMakeFails = {
        expr =
          let
            result = valueType.tryMake invalid.input;
          in
          {
            inherit (result) success value;
            kinds = getErrorKinds result;
          };
        expected = {
          success = false;
          value = null;
          kinds = [ invalid.kind ];
        };
      };

      testMakeReturnsTryMakeValue = {
        expr = valueType.make full;
        expected = (valueType.tryMake full).value;
      };

      testMakeThrowsTryMakeError = {
        expr = valueType.make invalid.input;
        expectedError = {
          type = "ThrownError";
          msg = "\\[${invalid.kind}]";
        };
      };

      # AGENTS.md (5): `toJSONValue` output is a valid `make` input, and
      # serializing it again gives the same value.
      testRoundTripMinimal = {
        expr = serialize (serialize minimal);
        expected = serialize minimal;
      };

      # A full input is already serialized, so it survives unchanged.
      testRoundTripFull = {
        expr = serialize full;
        expected = full;
      };

      testOptionTypeMatchesMakeMinimal = {
        expr = evalSubmodule wanwatch.types.${typeName} minimal;
        expected = serialize minimal;
      };

      testOptionTypeMatchesMakeFull = {
        expr = evalSubmodule wanwatch.types.${typeName} full;
        expected = full;
      };
    };
in
lib.mapAttrs contractTests invalidInputs
