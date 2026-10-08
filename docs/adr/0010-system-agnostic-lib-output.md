# System-agnostic `lib` output and a hand-written flake

The flake's `lib` output is a single attribute set, not wrapped in `forAllSystems`, because it operates only on Nix values and would be identical for every system. Consumers that need it bound to a different nixpkgs call `import (wanwatch + "/lib") { lib = …; libnet = …; }` directly. The flake uses a hand-written `forAllSystems` rather than `flake-utils` or `flake-parts`, matching the sibling `nix-nftzones` and `nix-libnet` flakes and keeping inputs minimal.
