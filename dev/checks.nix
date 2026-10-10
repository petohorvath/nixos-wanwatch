{
  inputs,
  packages,
  pkgs,
  treefmt,
}:
import ../tests { inherit inputs packages pkgs; }
// {
  format = treefmt.config.build.check inputs.self;
}
