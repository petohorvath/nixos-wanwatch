# Adopt the devnix-labs code quality standard

Formatting and linting follow the code quality standard shared by the devnix-labs repositories, so every repository wires the same tools the same way. The `dev` partition imports treefmt-nix's flake-parts module, configured in `dev/treefmt.nix`, which provides `nix fmt` and the `treefmt` check from one configuration. treefmt runs nixfmt and deadnix for Nix, gofumpt and goimports for Go, shfmt and shellcheck for shell, ruff-format and ruff-check for Python, prettier for Markdown, YAML, and JSON, and actionlint for the workflows. Before, `dev/formatter.nix` ran nixfmt, gofumpt, and goimports through `treefmt-nix.lib.evalModule`, and the `lint` check ran deadnix.

statix and golangci-lint stay in the `lint` check. treefmt-nix runs `statix fix`, which skips the lints that statix cannot fix, and passes golangci-lint single files with `--fix` instead of analyzing whole packages against the vendored modules.

The development shell takes treefmt and its programs from the module's development shell, so a program added to `dev/treefmt.nix` reaches the shell without another edit.

## Consequences

The `formatting` check is now `treefmt`. It fails when a program would change a file or reports a finding that it cannot fix, and its build log shows the diff that `nix fmt` would make.

treefmt-nix takes the tools from the nixpkgs in use, so the project policy's unstable run also runs unstable's tool versions. A tool release can fail that run without a change here; the fix, or a tool setting that keeps the old behavior, lands before the locked nixpkgs reaches that release.

Prettier runs with its defaults and no `.prettierrc.json`, so an editor that formats on save gives the same result as `nix fmt`.

Prettier skips the `*.golden.json` files: the state golden is compared byte for byte with the daemon's encoder output, and the daemon-config golden is regenerated with `jq .`.
