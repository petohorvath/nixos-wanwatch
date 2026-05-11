/*
  Shared helpers for unit test files. Imported per-file as:

    helpers = import ./helpers.nix { inherit pkgs; };
    inherit (helpers) evalThrows errorMatches;

  Lives alongside `runner.nix` so per-module test files (probe.nix,
  wan.nix, …) can pull common assertion utilities without
  reinventing them.
*/
{ pkgs }:
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
}
