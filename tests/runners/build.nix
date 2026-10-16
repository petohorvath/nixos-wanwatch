# test-standard v1: copied from the test-standard skill; do not edit here.
/*
  Turns the build entry point's tests into entries named
  `build-<test>`, so each test is reported and built by name.

  `pkgs`: the package set providing `linkFarm`.
  `repo`: the repo name, prefixed to the derivation names.
  `tests`: the build entry point's attrset of build tests. A test is a
  derivation, or a nested attrset of derivations when one program runs
  over many cases.

  Returns `{ "build-<test>" = derivation; … }`. An attrset test becomes
  a `linkFarm` whose links are the case attribute paths joined by `/`,
  declaring the union of the cases' `requiredSystemFeatures`.
*/
{ pkgs, repo }:
tests:
let
  inherit (pkgs) lib;

  # The derivations of a nested attrset of cases, named by path.
  listCases =
    prefix: cases:
    lib.concatLists (
      lib.mapAttrsToList (
        caseName: value:
        let
          path = prefix ++ [ caseName ];
        in
        if lib.isDerivation value then
          [
            {
              name = lib.concatStringsSep "/" path;
              path = value;
            }
          ]
        else
          listCases path value
      ) cases
    );

  toEntry =
    testName: test:
    if lib.isDerivation test then
      test
    else
      let
        cases = listCases [ ] test;
      in
      if cases == [ ] then
        throw "${repo}: build test ${testName} returns no derivations"
      else
        (pkgs.linkFarm "${repo}-build-${testName}" cases).overrideAttrs {
          requiredSystemFeatures = lib.unique (
            lib.concatMap (case: case.path.requiredSystemFeatures or [ ]) cases
          );
        };
in
lib.mapAttrs' (testName: test: lib.nameValuePair "build-${testName}" (toEntry testName test)) tests
