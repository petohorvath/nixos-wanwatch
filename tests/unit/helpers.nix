/*
  Test helpers shared by the unit suites; `default.nix` passes them to
  every suite as `helpers`. The `…Tests` helpers turn a case table into
  nix-unit tests whose results list the misjudged cases, so a failure
  names them.
*/
{ lib }:
let
  /*
    Evaluate a NixOS option type against a value, so suites can test
    option types without a full NixOS evaluation.

    `type`: the option type under test.
    `value`: the definition to merge into an option of that type.

    Returns the merged value, including submodule defaults. Throws when
    the type rejects the value.

      evalType types.identifier "primary"  # => "primary"
  */
  evalType =
    type: value:
    (lib.evalModules {
      modules = [
        {
          options.value = lib.mkOption {
            inherit type;
            description = "Value checked against the option type under test.";
          };
        }
        { config.value = value; }
      ];
    }).config.value;

  /*
    Test whether an option type accepts a value. The result is forced
    with `builtins.deepSeq` because `builtins.tryEval` is shallow:
    element checks inside `listOf` would otherwise escape it.

    `type`: the option type under test.
    `value`: the definition to merge into an option of that type.

    Returns true when `evalType type value` evaluates completely.
  */
  isAcceptedByType =
    type: value:
    (builtins.tryEval (
      let
        result = evalType type value;
      in
      builtins.deepSeq result result
    )).success;

  /*
    List the error kinds of a `tryResult`. `formatErrors` renders each
    error as `[<kind>] <message>`, so every bracketed camelCase word is
    a kind, including those of nested errors that a wrapping kind such
    as `wanInvalidProbe` forwards.

    `result`: the `tryResult` returned by a `tryMake`.

    Returns the kinds in report order, or `[ ]` on success.

      getErrorKinds (wanwatch.probe.tryMake { targets = { }; })
      # => [ "probeNoTargets" ]
  */
  getErrorKinds =
    result:
    if result.success then
      [ ]
    else
      lib.pipe result.error [
        (builtins.split "\\[([a-z][a-zA-Z]*)]")
        (builtins.filter builtins.isList)
        (map builtins.head)
      ];

  # Turns a case name such as `emptyBuckets` into `testEmptyBuckets`,
  # the prefix nix-unit runs.
  toTestName =
    caseName:
    "test" + lib.toUpper (builtins.substring 0 1 caseName) + builtins.substring 1 (-1) caseName;

  /*
    Build tests that a validator accepts every valid case and rejects
    every invalid one. Each test lists the cases it misjudged and
    expects none.

    `isAccepted`: returns true when the validator accepts a case.
    `isRejected`: returns true when the validator rejects a case as
    intended.
    `valid`, `invalid`: the cases; a missing side adds no test.

    Returns `{ testAcceptsValid; testRejectsInvalid; }`.
  */
  caseTests =
    { isAccepted, isRejected }:
    {
      valid ? [ ],
      invalid ? [ ],
    }:
    lib.optionalAttrs (valid != [ ]) {
      testAcceptsValid = {
        expr = builtins.filter (value: !isAccepted value) valid;
        expected = [ ];
      };
    }
    // lib.optionalAttrs (invalid != [ ]) {
      testRejectsInvalid = {
        expr = builtins.filter (value: !isRejected value) invalid;
        expected = [ ];
      };
    };

  /*
    Build case tests for a predicate.

    `predicate`: a function returning a Boolean.
    `cases`: `{ valid; invalid; }`, such as a `fixtures.cases` table.

    Returns the tests described for `caseTests`.
  */
  predicateTests =
    predicate:
    caseTests {
      isAccepted = predicate;
      isRejected = value: !predicate value;
    };
in
{
  inherit
    evalType
    getErrorKinds
    isAcceptedByType
    predicateTests
    ;

  /*
    Evaluate a submodule type as `services.wanwatch` does, under an
    attribute key, so a read-only `name` option takes the key.

    `type`: a submodule option type, such as `types.wan`.
    `input`: a value-type input; its `name`, if any, becomes the key
    and the rest the definition.

    Returns the merged submodule value.
  */
  evalSubmodule =
    type: input:
    let
      key = input.name or "value";
    in
    (evalType (lib.types.attrsOf type) { ${key} = removeAttrs input [ "name" ]; }).${key};

  /*
    Build case tests for an option type.

    `type`: the option type under test.
    `cases`: `{ valid; invalid; }`, such as a `fixtures.cases` table.

    Returns the tests described for `caseTests`.
  */
  typeTests = type: predicateTests (isAcceptedByType type);

  /*
    Build case tests for one field of a value type: `tryMake` accepts
    each valid value and rejects each invalid one with exactly `kind`.
    Suites bind the first argument once per value type.

    `tryMake`: the value type's `tryMake`.
    `input`: a valid input whose field the cases replace.
    `field`: the field's attribute path, dot-separated, such as
    `"hysteresis.consecutiveUp"`.
    `kind`: the error kind an invalid value must produce.
    `cases`: `{ valid; invalid; }`, such as a `fixtures.cases` table.

    Returns the tests described for `caseTests`.

      probeFieldTests = fieldTests { inherit (probe) tryMake; input = …; };
      probeFieldTests "method" "probeInvalidMethod" cases.methods
  */
  fieldTests =
    { tryMake, input }:
    field: kind:
    let
      tryMakeWith =
        value: tryMake (lib.recursiveUpdate input (lib.setAttrByPath (lib.splitString "." field) value));
    in
    caseTests {
      isAccepted = value: (tryMakeWith value).success;
      isRejected = value: getErrorKinds (tryMakeWith value) == [ kind ];
    };

  /*
    Build one test per rejected input, each expecting `tryMake` to
    report exactly the kind it is filed under.

    `tryMake`: the value type's `tryMake`.
    `inputsByKind`: `{ <kind> = { <caseName> = <input>; }; }`.

    Returns `{ <kind> = { test<CaseName> = <test>; }; }`.
  */
  rejectionTests =
    tryMake:
    lib.mapAttrs (
      kind:
      lib.mapAttrs' (
        caseName: input:
        lib.nameValuePair (toTestName caseName) {
          expr = getErrorKinds (tryMake input);
          expected = [ kind ];
        }
      )
    );
}
