/*
  Shared assertion helpers for the unit-test suites. `default.nix`
  passes them to every suite as `helpers`.
*/
{ pkgs }:
let
  inherit (pkgs) lib;

  /*
    Evaluate a NixOS option type against a value, so suites can test
    option types without a full NixOS evaluation.

    `type`: the option type under test.
    `value`: the definition to merge into an option of that type.

    Returns the merged value, including submodule defaults. Throws when
    the type rejects the value; use `evalTypeFails` for negative cases.

      evalType types.identifier "primary"  # => "primary"
      evalType types.probe { targets.v4 = [ "1.1.1.1" ]; }
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
in
{
  /*
    Test whether evaluating an expression throws, for assert-it-throws
    cases.

    `expr`: the expression to evaluate.

    Returns true when `builtins.tryEval` reports a failure. Evaluation
    is shallow; force nested values before passing them in.
  */
  evalThrows = expr: !(builtins.tryEval expr).success;

  /*
    Test whether an aggregated error string carries an error kind.
    `internal.primitives.formatErrors` renders errors as
    `<context>: [<kind>] <message>; …`, so a literal `[kind]`
    substring confirms that violation is present.

    `kind`: the error kind, without brackets.
    `message`: the error string to search.

    Returns true when the message contains `[kind]`.
  */
  errorMatches = kind: message: lib.hasInfix "[${kind}]" message;

  inherit evalType;

  /*
    Test whether an option type rejects a value.

    `type`: the option type under test.
    `value`: the definition to merge into an option of that type.

    Returns true when `evalType type value` throws. The result is
    forced with `builtins.deepSeq` because `builtins.tryEval` is
    shallow: element checks inside `listOf` would otherwise escape it
    and fail later, when the runner formats the result.
  */
  evalTypeFails =
    type: value:
    !(builtins.tryEval (
      let
        result = evalType type value;
      in
      builtins.deepSeq result result
    )).success;

  /*
    Return the error of a failed `tryMake`, so suites can match error
    kinds without unpacking the result.

    `valueType`: a value-type module such as `wanwatch.probe`.
    `input`: the attrset passed to `valueType.tryMake`.

    Returns the error string, or null when construction succeeds.

      tryError = helpers.tryError wanwatch.probe;
      tryError { targets = { }; }  # => "probe.make: [probeNoTargets] …"
  */
  tryError =
    valueType: input:
    let
      result = valueType.tryMake input;
    in
    if result.success then null else result.error;
}
