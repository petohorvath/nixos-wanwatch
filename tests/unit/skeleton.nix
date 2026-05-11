/*
  Skeleton meta-test. Asserts that every value-type module in the
  wanwatch lib exports the full §5.1 API skeleton:

    make / tryMake / is<T> / eq / compare / lt / le / gt / ge /
    min / max / toJSON

  This is a guard rail, not a behaviour test. Per-type test files
  (`probe.nix`, `wan.nix`, …) verify *what* each function does;
  this file just verifies *that* every required function exists
  and is callable. Catches the common "I added a new type and
  forgot `compare`" regression at flake-check time.

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

  # ===== The required skeleton =====

  requiredCommonFunctions = [
    "make"
    "tryMake"
    "eq"
    "compare"
    "lt"
    "le"
    "gt"
    "ge"
    "min"
    "max"
    "toJSON"
  ];

  # ===== Value-type registry =====
  #
  # Entry: { module = wanwatch.<name>; predicate = "is<Title>"; }
  # Add a new line per Pass 3+ value type (group, member, …).

  valueTypes = {
    probe = {
      module = wanwatch.probe;
      predicate = "isProbe";
    };
    member = {
      module = wanwatch.member;
      predicate = "isMember";
    };
    wan = {
      module = wanwatch.wan;
      predicate = "isWan";
    };
  };

  # ===== Test generation =====

  mkPresenceTest = typeName: module: fnName: {
    name = "testSkeleton_${typeName}_exports_${fnName}";
    value = {
      expr = module ? ${fnName} && builtins.isFunction module.${fnName};
      expected = true;
    };
  };

  mkPredicateTest = typeName: module: predicateName: {
    name = "testSkeleton_${typeName}_exports_${predicateName}";
    value = {
      expr = module ? ${predicateName} && builtins.isFunction module.${predicateName};
      expected = true;
    };
  };

  testsForType =
    typeName: spec:
    builtins.listToAttrs (
      [ (mkPredicateTest typeName spec.module spec.predicate) ]
      ++ map (fnName: mkPresenceTest typeName spec.module fnName) requiredCommonFunctions
    );

  allSkeletonTests = builtins.foldl' (
    acc: typeName: acc // testsForType typeName valueTypes.${typeName}
  ) { } (builtins.attrNames valueTypes);
in
allSkeletonTests
