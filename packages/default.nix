{
  pkgs,
  version,
  revision,
}:
let
  wanwatchd = pkgs.callPackage ./wanwatchd/package.nix { inherit version revision; };
in
{
  inherit wanwatchd;
  default = wanwatchd;
}
