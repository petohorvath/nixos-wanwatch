# test-standard v1: copied from the test-standard skill; do not edit here.
/*
  Turns the VM entry point's scenarios into entries named
  `vm-<scenario>`.

  `pkgs`: the package set providing `testers.runNixOSTest`.
  `repo`: the repo name, prefixed to the test names.
  `scenarios`: the VM entry point's attrset of NixOS test modules,
  without `name`.

  Returns `{ "vm-<scenario>" = derivation; … }`.
*/
{ pkgs, repo }:
scenarios:
pkgs.lib.mapAttrs' (
  scenario: module:
  pkgs.lib.nameValuePair "vm-${scenario}" (
    pkgs.testers.runNixOSTest {
      imports = [ module ];
      name = "${repo}-${scenario}";
    }
  )
) scenarios
