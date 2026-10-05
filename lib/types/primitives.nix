/*
  Option-type primitives shared by the per-concept type modules and
  exported as `wanwatch.types.<name>`:

    identifier     — string matching `[a-zA-Z][a-zA-Z0-9-]*`, the same
                     shape `internal.primitives.isValidName` accepts
    positiveInt    — integer ≥ 1
    pctInt         — integer in [0, 100]
    fwmark         — netfilter fwmark in [1000, 32767]
    routingTableId — routing-table ID in [1000, 32767]

  TODO(v0.2): move `fwmark` and `routingTableId` to nix-libnet, where
  other route- and rule-installing modules can use them (TODO.md).
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
