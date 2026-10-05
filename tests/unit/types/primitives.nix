/*
  Unit tests for `lib/types/primitives.nix`: each shared option type
  is evaluated against valid and invalid values.
*/
{ helpers, wanwatch, ... }:
let
  inherit (helpers) evalType evalTypeFails;
  inherit (wanwatch) types;
in
{
  # ===== identifier =====

  testIdentifierAcceptsAlpha = {
    expr = evalType types.identifier "primary";
    expected = "primary";
  };

  testIdentifierAcceptsHyphen = {
    expr = evalType types.identifier "home-uplink";
    expected = "home-uplink";
  };

  testIdentifierAcceptsAlphanumeric = {
    expr = evalType types.identifier "wan42";
    expected = "wan42";
  };

  testIdentifierRejectsEmpty = {
    expr = evalTypeFails types.identifier "";
    expected = true;
  };

  testIdentifierRejectsLeadingDigit = {
    expr = evalTypeFails types.identifier "1primary";
    expected = true;
  };

  testIdentifierRejectsSpace = {
    expr = evalTypeFails types.identifier "two words";
    expected = true;
  };

  testIdentifierRejectsUnderscore = {
    # Deliberately stricter than libnet's interface-name check; see
    # `isValidName` in lib/internal/primitives.nix.
    expr = evalTypeFails types.identifier "home_uplink";
    expected = true;
  };

  testIdentifierRejectsDot = {
    expr = evalTypeFails types.identifier "home.uplink";
    expected = true;
  };

  # ===== positiveInt =====

  testPositiveIntAcceptsOne = {
    expr = evalType types.positiveInt 1;
    expected = 1;
  };

  testPositiveIntAcceptsLarge = {
    expr = evalType types.positiveInt 32767;
    expected = 32767;
  };

  testPositiveIntRejectsZero = {
    expr = evalTypeFails types.positiveInt 0;
    expected = true;
  };

  testPositiveIntRejectsNegative = {
    expr = evalTypeFails types.positiveInt (-1);
    expected = true;
  };

  testPositiveIntRejectsString = {
    expr = evalTypeFails types.positiveInt "1";
    expected = true;
  };

  # ===== pctInt =====

  testPctIntAcceptsZero = {
    expr = evalType types.pctInt 0;
    expected = 0;
  };

  testPctIntAcceptsHundred = {
    expr = evalType types.pctInt 100;
    expected = 100;
  };

  testPctIntAcceptsMid = {
    expr = evalType types.pctInt 50;
    expected = 50;
  };

  testPctIntRejectsNegative = {
    expr = evalTypeFails types.pctInt (-1);
    expected = true;
  };

  testPctIntRejectsAbove100 = {
    expr = evalTypeFails types.pctInt 101;
    expected = true;
  };

  testPctIntRejectsFloat = {
    expr = evalTypeFails types.pctInt 50.5;
    expected = true;
  };

  # ===== fwmark =====

  testFwmarkAcceptsLowerBound = {
    expr = evalType types.fwmark 1000;
    expected = 1000;
  };

  testFwmarkAcceptsUpperBound = {
    expr = evalType types.fwmark 32767;
    expected = 32767;
  };

  testFwmarkAcceptsMid = {
    expr = evalType types.fwmark 16000;
    expected = 16000;
  };

  testFwmarkRejectsZero = {
    # `meta mark set 0` clears the mark — using it as a routing key
    # matches every unmarked packet, never a useful policy choice.
    expr = evalTypeFails types.fwmark 0;
    expected = true;
  };

  testFwmarkRejectsBelowLowerBound = {
    # The 1000 floor buries the small-integer space ad-hoc scripts
    # commonly grab (mark 1, mark 10, …).
    expr = evalTypeFails types.fwmark 999;
    expected = true;
  };

  testFwmarkRejectsAboveUpperBound = {
    expr = evalTypeFails types.fwmark 32768;
    expected = true;
  };

  testFwmarkRejectsNegative = {
    expr = evalTypeFails types.fwmark (-1);
    expected = true;
  };

  testFwmarkRejectsString = {
    expr = evalTypeFails types.fwmark "1000";
    expected = true;
  };

  testFwmarkRejectsFloat = {
    expr = evalTypeFails types.fwmark 1000.5;
    expected = true;
  };

  # ===== routingTableId =====
  # Shares its range with `fwmark`; the 1000 floor already excludes
  # the kernel-reserved ids 253, 254, and 255.

  testRoutingTableIdAcceptsLowerBound = {
    expr = evalType types.routingTableId 1000;
    expected = 1000;
  };

  testRoutingTableIdAcceptsUpperBound = {
    expr = evalType types.routingTableId 32767;
    expected = 32767;
  };

  testRoutingTableIdRejectsKernelReservedMain = {
    # Writing into `main` (254) would fight every other route
    # installer.
    expr = evalTypeFails types.routingTableId 254;
    expected = true;
  };

  testRoutingTableIdRejectsKernelReservedLocal = {
    # `local` (255) is auto-populated with this-host addresses;
    # writing into it breaks "is this packet for me" lookups.
    expr = evalTypeFails types.routingTableId 255;
    expected = true;
  };

  testRoutingTableIdRejectsAboveUpperBound = {
    expr = evalTypeFails types.routingTableId 32768;
    expected = true;
  };

  testRoutingTableIdRejectsZero = {
    expr = evalTypeFails types.routingTableId 0;
    expected = true;
  };
}
