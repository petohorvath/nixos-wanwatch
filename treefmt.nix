/*
  treefmt-nix configuration: `nix fmt` formats Nix with nixfmt and Go
  with gofumpt and goimports. CI runs it with `--fail-on-change`.
*/
_: {
  projectRootFile = "flake.nix";

  programs = {
    nixfmt.enable = true;
    gofumpt.enable = true;
    goimports.enable = true;
  };

  settings.global.excludes = [
    "LICENSE"
    "*.lock"
    "result"
    "result-*"
    ".direnv/**"
    # Vendored code keeps upstream formatting so `go mod vendor`
    # updates stay clean.
    "daemon/vendor/**"
  ];
}
