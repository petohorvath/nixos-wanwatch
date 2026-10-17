# treefmt-nix configuration: `nix fmt` formats and fixes the tree, and the
# `treefmt` check fails when it would change a file or a tool reports a
# finding it cannot fix.
{
  programs = {
    nixfmt.enable = true;
    deadnix.enable = true;
    actionlint.enable = true;
    prettier.enable = true;
    shfmt.enable = true;
    shellcheck.enable = true;
    ruff-format.enable = true;
    ruff-check.enable = true;
    gofumpt.enable = true;
    goimports.enable = true;
  };

  /*
    No formatter may change these files. Vendored code keeps upstream
    formatting so `go mod vendor` updates stay clean. The goldens keep
    the bytes their generators write: the state golden is compared
    byte for byte with Go's encoder output, and the daemon-config
    golden is regenerated with `jq .` (docs/specs/daemon-config.md).
  */
  settings.excludes = [
    "daemon/vendor/**"
    "*.golden.json"
  ];
}
