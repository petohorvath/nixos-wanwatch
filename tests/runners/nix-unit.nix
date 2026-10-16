# test-standard v1: copied from the test-standard skill; do not edit here.
/*
  Turns a nix-unit entry point into a check, so its suites run under
  `nix flake check` and the project policy's `test`.

  `inputs`: the flake inputs, including `self`.
  `pkgs`: the package set providing nix-unit.
  `repo`: the repo name, prefixed to the derivation name.
  `srcDir`: a source tree holding the files the suites read.

  The returned function takes:

  `kind`: `"unit"` or `"integration"`, the entry point under `tests/`.
  `name`: the entry name; the derivation is `<repo>-<name>`.
  `suites`: the top-level suites to run; null runs all.
  `extraArgs`: extra or overriding `--arg` values, as Nix expression
  strings.
  `requiredSystemFeatures`: features of builds the suites read.
  `postCheck`: shell commands run after the suites pass.
  `jobs`: nix-unit processes run at once; null uses `$NIX_BUILD_CORES`.

  Returns a derivation that builds when every selected test passes.
*/
{
  inputs,
  pkgs,
  repo,
  srcDir,
}:
{
  kind,
  name ? kind,
  suites ? null,
  extraArgs ? { },
  requiredSystemFeatures ? [ ],
  postCheck ? "",
  jobs ? null,
}:
let
  inherit (pkgs) lib;
  inherit (pkgs.stdenv.hostPlatform) system;

  entryPoint = "${srcDir}/tests/${kind}";
  entryArgs = builtins.functionArgs (import entryPoint);

  # The declared arguments that come from the flake inputs.
  inputArgs = lib.pipe inputs [
    (builtins.intersectAttrs entryArgs)
    (lib.filterAttrs (argName: _: argName != "system" && !(extraArgs ? ${argName})))
  ];

  # A flake input as a tree of store paths, keeping every transitive
  # input, so the sandbox fetches nothing.
  toTree =
    input:
    if input._type or null == "flake" then
      {
        path = input.outPath;
        inputs = builtins.mapAttrs (_: toTree) input.inputs;
      }
    else
      {
        path = input.outPath;
        flake = false;
      };

  # Store paths are written as path literals so the evaluator reads
  # them directly; their string context makes them build inputs.
  toTreeExpr =
    tree:
    if tree.flake or true then
      "{ path = ${tree.path}; inputs = { ${
        lib.concatStrings (
          lib.mapAttrsToList (
            inputName: subtree: "${lib.strings.escapeNixIdentifier inputName} = ${toTreeExpr subtree}; "
          ) tree.inputs
        )
      }}; }"
    else
      "{ path = ${tree.path}; flake = false; }";

  argExprs =
    builtins.mapAttrs (
      _: input: "(import ${../helpers/evaluation-inputs.nix}) ${toTreeExpr (toTree input)}"
    ) inputArgs
    // extraArgs;

  # The suites as the sandbox sees them, used only to list the groups.
  tests = import entryPoint (
    builtins.intersectAttrs entryArgs (
      inputArgs
      // {
        inherit system;
      }
      // builtins.mapAttrs (
        argName: expr: import (builtins.toFile "${argName}.nix" (builtins.unsafeDiscardStringContext expr))
      ) extraArgs
    )
  );
  selectedTests = if suites == null then tests else lib.getAttrs suites tests;

  # A group is an attrset that directly holds `test*` attributes.
  # nix-unit runs the tests nested below a group with it, so the walk
  # stops at the first group on each path.
  listGroups =
    prefix: attrs:
    if lib.any (lib.hasPrefix "test") (builtins.attrNames attrs) then
      [ (lib.showAttrPath prefix) ]
    else
      lib.concatLists (
        lib.mapAttrsToList (
          attrName: value: if builtins.isAttrs value then listGroups (prefix ++ [ attrName ]) value else [ ]
        ) attrs
      );
  groups = listGroups [ ] selectedTests;

  nixUnitArgs = lib.escapeShellArgs (
    [
      entryPoint
      "--argstr"
      "system"
      system
    ]
    ++ lib.concatLists (
      lib.mapAttrsToList (argName: expr: [
        "--arg"
        argName
        expr
      ]) argExprs
    )
  );

  maxJobs = if jobs == null then "$NIX_BUILD_CORES" else toString jobs;

  # Runs one group and prints its log once it finishes, so the logs of
  # parallel groups do not interleave line by line.
  runGroup = pkgs.writeShellScript "${repo}-${name}-run-group" ''
    group=$1
    args=(--show-trace --eval-store "$TMPDIR/eval-store")
    ${lib.optionalString (lib.versionOlder pkgs.nix-unit.version "2.35") ''
      # nix-unit releases before 2.35 need a directory for GC roots.
      args+=(--gc-roots-dir "$(mktemp -d "$TMPDIR/gc-roots.XXXXXX")")
    ''}
    log=$(mktemp "$TMPDIR/nix-unit.XXXXXX")
    if nix-unit "''${args[@]}" ${nixUnitArgs} --attr "$group" >"$log" 2>&1; then
      cat "$log"
    else
      cat "$log"
      echo "${repo}-${name}: group $group failed" >&2
      exit 1
    fi
  '';
in
assert lib.assertMsg (groups != [ ]) "${repo}-${name}: tests/${kind} has no test groups";
pkgs.runCommand "${repo}-${name}"
  {
    inherit requiredSystemFeatures;
    nativeBuildInputs = [ pkgs.nix-unit ];
  }
  ''
    export HOME=$TMPDIR
    # xargs runs every group and fails if any of them failed.
    printf '%s\0' ${lib.escapeShellArgs groups} |
      xargs -0 -n 1 -P "${maxJobs}" ${runGroup}
    ${postCheck}
    touch "$out"
  ''
