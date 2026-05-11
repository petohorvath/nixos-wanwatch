/*
  Unit tests for `lib/types/primitives.nix` (exposed at
  `wanwatch.types.{identifier,positiveInt,pctInt}`). Each type is
  exercised via `lib.evalModules` against valid and invalid inputs.

  Mirrors the nftzones convention `tests/unit/types/<name>.nix`.
*/
{ pkgs, libnet, ... }:
let
  wanwatch = import ../../../lib {
    inherit (pkgs) lib;
    inherit libnet;
  };
  inherit (wanwatch) types;

  helpers = import ../helpers.nix { inherit pkgs; };
  inherit (helpers) evalType evalTypeFails;
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
    # Stricter than libnet's interface-name check on purpose; matches
    # nftzones' `primitives.identifier`. See lib/internal/primitives.nix
    # `isValidName` for the rationale.
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
}
