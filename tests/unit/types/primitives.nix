# Tests for `lib/types/primitives.nix`. Each option type tests the same
# case table as the validators, so the two layers agree.
{
  fixtures,
  helpers,
  wanwatch,
  ...
}:
let
  inherit (fixtures) cases;
  inherit (helpers) typeTests;
  inherit (wanwatch) types;
in
{
  identifier = typeTests types.identifier cases.identifiers;
  positiveInt = typeTests types.positiveInt cases.positiveInts;
  pctInt = typeTests types.pctInt cases.percentages;
  fwmark = typeTests types.fwmark cases.markTableIds;
  routingTableId = typeTests types.routingTableId cases.markTableIds;
}
