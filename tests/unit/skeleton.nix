/*
  Skeleton meta-test: every value-type module (probe, member, wan,
  group) exports the common `make` / `tryMake` / `toJSONValue` API.
  The per-type suites test what these functions do; this suite catches
  a new value type that omits one of them.

  Pure-function modules (selector, config) use purpose-specific APIs
  and are not checked here.
*/
{ pkgs, wanwatch, ... }:
let
  inherit (pkgs) lib;

  requiredFunctionNames = [
    "make"
    "tryMake"
    "toJSONValue"
  ];

  valueTypes = {
    inherit (wanwatch)
      group
      member
      probe
      wan
      ;
  };

  makePresenceTest = typeName: valueType: functionName: {
    name = "testSkeleton_${typeName}_exports_${functionName}";
    value = {
      expr = valueType ? ${functionName} && builtins.isFunction valueType.${functionName};
      expected = true;
    };
  };
in
lib.pipe valueTypes [
  (lib.mapAttrsToList (
    typeName: valueType: map (makePresenceTest typeName valueType) requiredFunctionNames
  ))
  lib.flatten
  builtins.listToAttrs
]
