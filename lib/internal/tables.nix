/*
  internal/tables — routing-table-id allocator. Thin wrapper over
  `internal/allocator.nix` with wanwatch-specific constants.

  Range: 100-32766 inclusive (32667 candidate ids), with the
  kernel's well-known reserved tables excluded:
    253 = default
    254 = main
    255 = local
  `unspec=0` sits below the range so doesn't need explicit
  exclusion. Result: 32664 valid table ids.

  See PLAN §12 Open Question 2 for the rationale.

  ===== allocate =====

  `allocate names → { <group-name> = <table-id>; }` — deterministic,
  hash-and-probe over the full set. Same contract as `marks.allocate`:
  consumers reference by name, never by literal — the assignment
  for any given name can shift if the surrounding group set changes.
*/
{
  internal,
}:
{
  allocate = internal.allocator.mkAllocator {
    base = 100;
    size = 32667;
    forbidden = [
      253
      254
      255
    ];
  };
}
