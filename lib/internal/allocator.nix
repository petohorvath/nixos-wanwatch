/*
  internal/allocator — deterministic hash + linear-probe integer
  allocator for naming-set members.

  `marks` and `tables` both need the same shape: given a set of
  group names, produce a stable, name-keyed assignment of integers
  in a configured range, avoiding a configured set of forbidden
  ints. This module owns the algorithm; the two consumers wrap it
  with their own constants.

  Algorithm:

    1. Pre-compute `validIds` = sorted list of ints in
       `[base, base + size)` excluding `forbidden`.
    2. Sort input names lexicographically (so the result is
       independent of input order).
    3. For each name in sorted order:
         a. `idx = hashToInt name % length validIds`
         b. `candidate = validIds[idx]`
         c. If candidate is already taken, probe forward (wrap)
            until an unused id is found.
    4. Return `{ <name> = <int>; }`.

  Determinism contract: the result is a pure function of the
  *full* input name set. Adding or removing a name can change
  other names' assignments if probe paths cross — this is documented
  in PLAN §5.3 ("consumers should always reference by name, never
  by hardcoded literal").

  ===== mkAllocator =====

  `mkAllocator { base, size, forbidden ? [] }` returns a function
  `names → { <name> = <int>; }`. The closure pre-computes
  `validIds` once so repeated allocations over the same range
  share the work.

  Throws if `length names > length validIds` — more names than
  available ids is unrecoverable.

  ===== hashToInt =====

  `hashToInt name` returns the first 16 bits of
  `sha256(name)` as a non-negative integer in `[0, 65536)`.
  16 bits is enough for the ranges in v1 (≤ 32668 valid ids).
  Exposed for unit-testing.

  ===== validIds =====

  `validIds { base, size, forbidden }` returns the filtered list
  in sorted order. Exposed for unit-testing.
*/
{
  lib,
  libnet,
  internal,
}:
let
  # ===== Hex helpers =====

  # Map a single lowercase hex character to its integer value.
  # Uppercase variants get folded via `lib.toLower` before lookup.
  hexDigitMap = {
    "0" = 0;
    "1" = 1;
    "2" = 2;
    "3" = 3;
    "4" = 4;
    "5" = 5;
    "6" = 6;
    "7" = 7;
    "8" = 8;
    "9" = 9;
    a = 10;
    b = 11;
    c = 12;
    d = 13;
    e = 14;
    f = 15;
  };

  fromHex =
    s: lib.foldl' (acc: c: acc * 16 + hexDigitMap.${c}) 0 (lib.stringToCharacters (lib.toLower s));

  # ===== Hashing =====

  hashToInt = name: fromHex (builtins.substring 0 4 (builtins.hashString "sha256" name));

  # ===== Valid-id enumeration =====

  validIds =
    {
      base,
      size,
      forbidden ? [ ],
    }:
    builtins.filter (i: !(builtins.elem i forbidden)) (lib.range base (base + size - 1));

  # ===== Linear probe =====
  #
  # Walks forward from `idx` (with wrap) looking for an id not in
  # `taken`. `idCount` is passed in so the modulo target is fixed
  # by the closure.
  probeFrom =
    ids: idCount: taken: idx:
    let
      candidate = builtins.elemAt ids idx;
    in
    if !(builtins.elem candidate taken) then
      candidate
    else
      probeFrom ids idCount taken (lib.mod (idx + 1) idCount);

  # ===== Allocator builder =====

  mkAllocator =
    {
      base,
      size,
      forbidden ? [ ],
    }:
    let
      ids = validIds {
        inherit base size forbidden;
      };
      idCount = builtins.length ids;
    in
    names:
    if builtins.length names > idCount then
      builtins.throw "wanwatch: allocator: ${toString (builtins.length names)} names exceeds ${toString idCount} available ids in [${toString base}, ${toString (base + size - 1)}] (forbidden: ${builtins.toJSON forbidden})"
    else
      let
        sortedNames = lib.sort lib.lessThan names;
        foldFn =
          acc: name:
          let
            startIdx = lib.mod (hashToInt name) idCount;
            assigned = probeFrom ids idCount acc.taken startIdx;
          in
          {
            taken = acc.taken ++ [ assigned ];
            result = acc.result // {
              ${name} = assigned;
            };
          };
        final = lib.foldl' foldFn {
          taken = [ ];
          result = { };
        } sortedNames;
      in
      final.result;
in
{
  inherit
    mkAllocator
    hashToInt
    validIds
    ;
}
