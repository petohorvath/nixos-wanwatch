# mkShell rather than mkShellNoCC: `go test -race` needs gcc.
{
  deadnix,
  formatter,
  git,
  go,
  gofumpt,
  golangci-lint,
  gopls,
  gotools,
  mkShell,
  nil,
  nix,
  nix-unit,
  nixfmt,
  statix,
}:
mkShell {
  packages = [
    formatter
    nixfmt
    go
    gopls
    nix-unit
    gotools
    golangci-lint
    gofumpt
    statix
    deadnix
    nix
    git
    nil
  ];
}
