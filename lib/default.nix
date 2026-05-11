/*
  wanwatch — pure-Nix library composition.

  Top-level entry point. Assembles the core (pure-Nix; no
  `nixpkgs.lib` dependency) and exposes `withLib` as the opt-in
  extension point that injects `nixpkgs.lib` to unlock NixOS
  option types.

  Public surface:
    wanwatch.internal     — internal helpers (tag primitives, …)
    wanwatch.version      — current library version string
    wanwatch.withLib lib  — extends core with `types` (option types)

  See PLAN.md §5.1 for the full target API; modules are added
  bottom-up per PLAN.md §10 build order. Pass 1 covers
  `internal/types` only — value-type modules (wan, probe, group,
  member) and pure-function modules (selector, marks, tables,
  config, snippets) land in later passes.
*/
let
  core = {
    internal = {
      types = import ./internal/types.nix;
    };
    version = "0.1.0-dev";
  };
in
core // { withLib = import ./with-lib.nix core; }
