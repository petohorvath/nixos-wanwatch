/*
  Skeleton tests: every value type (probe, member, wan, group, global)
  implements the common `make` / `tryMake` / `toJSONValue` contract,
  and its submodule option type in `wanwatch.types` evaluates an input
  to what `make` builds from it, defaults included. Both layers reject
  unknown fields. The per-type suites test what is specific to each
  type. Add each new value type to `invalidInputs` and
  `fixtures.misspelledFields`.

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
  inherit (helpers) evalSubmodule getErrorKinds isAcceptedBySubmodule;

  # Attribute keys set the read-only `name` and `wan` options, so a
  # definition leaves out its key field, and a Group's Members leave
  # out their `wan`.
  keyFields.member = "wan";
  toDefinitions.group =
    input: input // { members = lib.mapAttrs (_: member: removeAttrs member [ "wan" ]) input.members; };

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
        members = { };
      };
      kind = "groupNoMembers";
    };
    global = {
      input.logLevel = "verbose";
      kind = "globalInvalidLogLevel";
    };
  };

  contractTests =
    typeName: invalid:
    let
      valueType = wanwatch.${typeName};
      inherit (fixtures.inputs.${typeName}) full minimal;
      misspelled = minimal // fixtures.misspelledFields.${typeName};
      serialize = input: valueType.toJSONValue (valueType.make input);
      optionType = wanwatch.types.${typeName};
      keyField = keyFields.${typeName} or "name";
      toDefinition = toDefinitions.${typeName} or lib.id;
      evalOptionType = input: evalSubmodule keyField optionType (toDefinition input);
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

      # A misspelled optional field must not silently fall back to its
      # default.
      testTryMakeRejectsUnknownField = {
        expr = getErrorKinds (valueType.tryMake misspelled);
        expected = [ "${typeName}UnknownField" ];
      };

      # An input that isn't an attrset must not fall back to defaults
      # or report a field error as if it were one.
      testTryMakeRejectsNonAttrset = {
        expr = map (input: getErrorKinds (valueType.tryMake input)) [
          5
          "debug"
          null
          [ ]
        ];
        expected = lib.replicate 4 [ "${typeName}NotAnAttrset" ];
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
        expr = evalOptionType minimal;
        expected = serialize minimal;
      };

      testOptionTypeMatchesMakeFull = {
        expr = evalOptionType full;
        expected = full;
      };

      testOptionTypeRejectsUnknownField = {
        expr = isAcceptedBySubmodule keyField optionType (toDefinition misspelled);
        expected = false;
      };
    };
in
lib.mapAttrs contractTests invalidInputs
