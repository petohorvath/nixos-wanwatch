/*
  Option-type primitives shared by the per-concept type modules and
  exported as `wanwatch.types.<name>`:

    identifier     — string matching `[a-zA-Z][a-zA-Z0-9-]*`, the same
                     shape `internal.primitives.isValidName` accepts
    positiveInt    — integer ≥ 1
    pctInt         — integer in [0, 100]
    fwmark         — netfilter fwmark in [1000, 32767]
    routingTableId — routing-table ID in [1000, 32767]

  `fwmark` and `routingTableId` are stable under ADR-0018: their names
  stay, and the values they accept never shrink in 0.x. The other names
  follow the options' 0.x policy.

  TODO(v0.2): if nix-libnet gains fwmark and routing-table types, build
  these on them only as a restriction to wanwatch's [1000, 32767] range
  (TODO.md).
*/
{ lib }:
{
  # Replaces the regex in `strMatching`'s description with a readable
  # one; `//` keeps the type's functor intact.
  identifier = lib.types.strMatching "[a-zA-Z][a-zA-Z0-9-]*" // {
    description = "wanwatch identifier (matching [a-zA-Z][a-zA-Z0-9-]*)";
  };

  # nixpkgs' integer types already have readable descriptions.
  positiveInt = lib.types.ints.positive;
  pctInt = lib.types.ints.between 0 100;

  /*
    Starting at 1000 keeps marks and tables clear of the reserved
    tables 253–255 and of the small numbers ad-hoc scripts use. The two
    types share a range but stay separate because they name different
    kernel concepts.
  */
  fwmark = lib.types.ints.between 1000 32767;
  routingTableId = lib.types.ints.between 1000 32767;
}
