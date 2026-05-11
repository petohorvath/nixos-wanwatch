/*
  Shared helpers for unit test files. Imported per-file as:

    helpers = import ./helpers.nix { inherit pkgs; };
    inherit (helpers) evalThrows errorMatches;

  Lives alongside `runner.nix` so per-module test files (probe.nix,
  wan.nix, …) can pull common assertion utilities without
  reinventing them.
*/
{ pkgs }:
let
  evalType =
    type: config:
    (pkgs.lib.evalModules {
      modules = [
        {
          options.value = pkgs.lib.mkOption { inherit type; };
        }
        { config.value = config; }
      ];
    }).config.value;
in
{
  /*
    True iff `expr` raises during evaluation. Standard wrapper over
    `builtins.tryEval` for assert-it-throws cases.
  */
  evalThrows = expr: !(builtins.tryEval expr).success;

  /*
    Substring match for the bracketed error-kind tag emitted by
    `internal.types.formatErrors`. The error string takes the shape
    `<ctx>: [<kind>] <msg>; [<kind2>] <msg2>; …`, so a literal
    `[kind]` substring confirms presence of that violation.
  */
  errorMatches = kind: msg: pkgs.lib.hasInfix "[${kind}]" msg;

  /*
    Parametrized over a value-type module (probe, wan, …): returns
    the error string from a failed `tryMake`, or `null` on success.

    Usage:
      tryError = helpers.tryError probe;
      tryError { targets = [ ]; }   # → "probe.make: [probeNoTargets] ..."
  */
  tryError =
    module: user:
    let
      r = module.tryMake user;
    in
    if r.success then null else r.error;

  /*
    Evaluate a NixOS option type against a config value. Returns
    the evaluated result (post-defaults, post-coercion). Throws
    when the type rejects the input; pair with `evalTypeFails` for
    negative cases.

    Usage:
      evalType types.identifier "primary"  # → "primary"
      evalType types.probe { targets = [ "1.1.1.1" ]; }
  */
  inherit evalType;

  /*
    True iff evaluating `type` against `config` throws — the
    type-rejection assertion for `evalType`.

    `builtins.tryEval` only forces one level; without `deepSeq`,
    lazy thunks (e.g. element-level checks inside `listOf`) slip
    past and the test runner later overflows trying to format the
    unforced result. Matches nftzones' `evalFails` pattern.
  */
  evalTypeFails =
    type: config:
    !(builtins.tryEval (
      let
        r = evalType type config;
      in
      builtins.deepSeq r r
    )).success;
}
