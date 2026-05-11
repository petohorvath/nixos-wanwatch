/*
  internal/marks — fwmark allocator. Thin wrapper over
  `internal/allocator.nix` with wanwatch-specific constants.

  Range: `0x64`-`0x7FFF` (100-32767, inclusive). 32668 distinct
  marks available. No forbidden values inside the range —
  the kernel does not reserve any specific marks; the range start
  at 100 (`0x64`) avoids `0`-prefixed values that some tools
  treat as "no mark set".

  See PLAN §12 Open Question 2 for the rationale.

  ===== allocate =====

  `allocate names → { <group-name> = <mark>; }` — deterministic,
  hash-and-probe over the full set. Consumers reference allocated
  marks by name (`services.wanwatch.marks.<group>`), never by
  hardcoded literal — the assignment for a given name can shift
  when the surrounding group set changes.
*/
{
  lib,
  libnet,
  internal,
}:
{
  allocate = internal.allocator.mkAllocator {
    base = 100;
    size = 32668;
  };
}
