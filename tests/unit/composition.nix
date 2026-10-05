/*
  Unit tests for `lib/default.nix`: the top-level composition exposes
  the version, the `internal` and `types` namespaces, and the
  value-type aliases.
*/
{ wanwatch, ... }:
{
  # ===== Top-level composition =====

  testVersionExposed = {
    expr = wanwatch.version;
    expected = "0.1.0";
  };

  testInternalNamespacesReachable = {
    # Each module has its own suite; this only checks the wiring.
    expr = builtins.all (name: wanwatch.internal ? ${name}) [
      "primitives"
      "probe"
      "wan"
    ];
    expected = true;
  };

  # ===== types namespace =====

  testTypesNamespaceReachable = {
    expr = wanwatch ? types;
    expected = true;
  };

  testTypesNamespaceHasMembers = {
    # `types` merges the per-concept type files; `tests/unit/types/`
    # covers their contents.
    expr = builtins.all (name: wanwatch.types ? ${name}) [
      "identifier"
      "positiveInt"
      "pctInt"
    ];
    expected = true;
  };

  testProbeAndWanReachable = {
    expr = builtins.all (name: wanwatch ? ${name}) [
      "probe"
      "wan"
    ];
    expected = true;
  };
}
