#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
config="$repo_root/.github/dependabot.yml"
legacy_workflow="$repo_root/.github/workflows/update-nixpkgs.yml"
flake="$repo_root/flake.nix"

python3 - "$config" "$legacy_workflow" "$flake" <<'PY'
from pathlib import Path
import re
import sys

config_path = Path(sys.argv[1])
legacy_workflow = Path(sys.argv[2])
flake_path = Path(sys.argv[3])

if not config_path.is_file():
    raise SystemExit(f"missing Dependabot config: {config_path}")
if legacy_workflow.exists():
    raise SystemExit(
        "write-capable nixpkgs update workflow must be absent when native Dependabot is configured"
    )

text = config_path.read_text()
checks = {
    "version 2": r'^version:\s*2\s*$',
    "Nix ecosystem": r'^\s*-\s*package-ecosystem:\s*["\']nix["\']\s*$',
    "repository root": r'^\s*directory:\s*["\']/["\']\s*$',
    "main target": r'^\s*target-branch:\s*["\']main["\']\s*$',
    "monthly schedule": r'^\s*interval:\s*["\']monthly["\']\s*$',
    "single open PR": r'^\s*open-pull-requests-limit:\s*1\s*$',
    "nixpkgs allow rule": r'allow:\s*\n\s*-\s*dependency-name:\s*["\']nixpkgs["\']',
    "nixpkgs channel guard": r'ignore:\s*\n\s*-\s*dependency-name:\s*["\']nixpkgs["\']\s*\n\s*versions:\s*\n\s*-\s*["\']> 26\.05["\']',
    "commit prefix": r'^\s*prefix:\s*["\']flake["\']\s*$',
}
for name, pattern in checks.items():
    if not re.search(pattern, text, flags=re.MULTILINE):
        raise SystemExit(f"missing {name}: /{pattern}/")

ecosystems = re.findall(
    r'^\s*-\s*package-ecosystem:\s*["\']([^"\']+)["\']\s*$',
    text,
    flags=re.MULTILINE,
)
if ecosystems != ["nix"]:
    raise SystemExit(f"Dependabot config must manage only the Nix ecosystem, got: {ecosystems}")

allowed = re.findall(
    r'^\s*-\s*dependency-name:\s*["\']([^"\']+)["\']\s*$',
    text.split("ignore:", 1)[0],
    flags=re.MULTILINE,
)
if allowed != ["nixpkgs"]:
    raise SystemExit(f"Dependabot must allow only the primary nixpkgs input, got: {allowed}")

flake_text = flake_path.read_text()
expected_url = 'nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";'
if expected_url not in flake_text:
    raise SystemExit(f"primary nixpkgs channel is not fixed to nixos-26.05: {expected_url}")

print("nixpkgs Dependabot contract passed")
PY
