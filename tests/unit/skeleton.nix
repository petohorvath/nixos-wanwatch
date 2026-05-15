/*
  Skeleton meta-test. Asserts that every value-type module in the
  wanwatch lib exports the load-bearing API:

    make / tryMake / toJSONValue

  This is a guard rail, not a behaviour test. Per-type test files
  (`probe.nix`, `wan.nix`, …) verify *what* each function does;
  this file just verifies *that* every required function exists
  and is callable. Catches the common "I added a new type and
  forgot `toJSONValue`" regression at flake-check time.

  Adding a new value type to `lib/` means adding one entry to
  `valueTypes` below — that's the entire incremental cost.

  Pure-function modules (selector, marks, tables, config,
  snippets) intentionally use a *different* skeleton (compute /
  allocate / render / …) and are not exercised here.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };

  requiredCommonFunctions = [
    "make"
    "tryMake"
    "toJSONValue"
  ];

  valueTypes = {
    inherit (wanwatch)
      probe
      member
      wan
      group
      ;
  };

  mkPresenceTest = typeName: module: fnName: {
    name = "testSkeleton_${typeName}_exports_${fnName}";
    value = {
      expr = module ? ${fnName} && builtins.isFunction module.${fnName};
      expected = true;
    };
  };

  testsForType =
    typeName: module:
    builtins.listToAttrs (map (fnName: mkPresenceTest typeName module fnName) requiredCommonFunctions);

  allSkeletonTests = builtins.foldl' (
    acc: typeName: acc // testsForType typeName valueTypes.${typeName}
  ) { } (builtins.attrNames valueTypes);
in
allSkeletonTests
