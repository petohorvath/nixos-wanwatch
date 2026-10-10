# wanwatchd is Linux-only, so other systems get no packages.
{ pkgs, version }:
let
  wanwatchd = pkgs.callPackage ./wanwatchd/package.nix {
    inherit version;
    revision = "unknown";
  };
in
pkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
  inherit wanwatchd;
  default = wanwatchd;
}
