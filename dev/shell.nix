/*
  `treefmtShell` supplies treefmt and every program that
  `dev/treefmt.nix` enables, so the list holds only the other tools.
  mkShell rather than mkShellNoCC: `go test -race` needs gcc.
*/
{
  git,
  go,
  golangci-lint,
  gopls,
  jq,
  mkShell,
  nil,
  nix,
  nix-unit,
  statix,
  treefmtShell,
}:
mkShell {
  inputsFrom = [ treefmtShell ];
  packages = [
    go
    gopls
    nix-unit
    golangci-lint
    statix
    nix
    git
    nil
    # Formats the regenerated daemon-config golden (docs/specs/daemon-config.md).
    jq
  ];
}
