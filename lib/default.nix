/*
  wanwatch — pure-Nix library composition.

  Top-level entry point. Assembles the core (pure-Nix; no
  `nixpkgs.lib` dependency at this layer) and exposes `withLib` as
  the opt-in extension point that injects `nixpkgs.lib` to unlock
  NixOS option types.

  Input contract:
    libnet — nix-libnet's pure-Nix core (the value at
             `inputs.libnet.lib`). Used by value-type modules
             (wan / probe / …) for IP, CIDR, and interface-name
             validation. Required even when consumers don't call
             `withLib`, because the core types reference libnet
             constructors at parse time.

  Public surface:
    wanwatch.internal     — internal helpers (tag primitives, …)
    wanwatch.version      — current library version string
    wanwatch.withLib lib  — extends core with `types` (option types)

  See PLAN.md §5.1 for the full target API; modules are added
  bottom-up per PLAN.md §10 build order. Pass 1 covered
  `internal/types` only — value-type modules (wan, probe, group,
  member) and pure-function modules (selector, marks, tables,
  config, snippets) land in later passes.
*/
{ libnet }:
let
  core = {
    internal = {
      types = import ./internal/types.nix;
    };
    version = "0.1.0-dev";
  };
in
core // { withLib = import ./with-lib.nix core libnet; }
