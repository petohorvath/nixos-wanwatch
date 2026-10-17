{ pkgs, version }:
let
  wanwatchd = pkgs.callPackage ./wanwatchd/package.nix {
    inherit version;
    revision = "unknown";
  };
in
{
  inherit wanwatchd;
  default = wanwatchd;
}
