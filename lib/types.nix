/*
  wanwatch.types — NixOS option types for module consumers.

  Module-option types (`lib.types.*`) for each wanwatch concept —
  `wanName`, `gateway`, `target`, etc. — plus `.mk` coercers that
  validate and return the original value. Imported directly by
  `lib/default.nix`; available unconditionally at
  `wanwatch.types.*`.

  Pass 1 boundary: empty stub. Real types land in Pass 5 (PLAN.md
  §10), at which point this file grows to flatten per-module type
  exports under a single `types` namespace — same pattern as
  `nix-nftzones/lib/default.nix`.
*/
{ lib, libnet }:
{
  types = { };
}
