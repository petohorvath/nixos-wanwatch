# Tests for `lib/default.nix`: the top-level exports. Each module has
# its own suite; this one checks only the wiring.
{ wanwatch, ... }:
{
  testVersion = {
    expr = wanwatch.version;
    expected = "0.1.0";
  };

  testExportsNamespaces = {
    expr = builtins.attrNames wanwatch;
    expected = [
      "config"
      "group"
      "internal"
      "member"
      "probe"
      "selector"
      "types"
      "version"
      "wan"
    ];
  };

  testExportsInternalModules = {
    expr = builtins.attrNames wanwatch.internal;
    expected = [
      "config"
      "group"
      "member"
      "primitives"
      "probe"
      "selector"
      "wan"
    ];
  };
}
