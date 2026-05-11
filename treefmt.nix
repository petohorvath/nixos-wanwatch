/*
  treefmt-nix configuration. One `nix fmt` formats every file in the
  repo regardless of language.

  Programs:
    - nixfmt   — RFC 166 canonical Nix formatter (*.nix)
    - gofumpt  — stricter gofmt superset (*.go)
    - goimports — manage import groups (*.go)

  CI gate: `nix fmt -- --fail-on-change`.
*/
_: {
  projectRootFile = "flake.nix";

  programs.nixfmt.enable = true;
  programs.gofumpt.enable = true;
  programs.goimports.enable = true;

  settings.global.excludes = [
    "LICENSE"
    "*.lock"
    "result"
    "result-*"
    ".direnv/**"
  ];
}
