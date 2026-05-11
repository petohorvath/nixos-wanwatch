/*
  internal — operational code for the wanwatch lib. Exposed under
  `wanwatch.internal.<module>`.

  Layout mirrors nftzones' three-tier internal hierarchy: leaf
  modules import nothing from sibling internals; later layers
  receive previously-built modules via `internal = …`. This file
  pipes the layers together.

  Layer order:
    1. `primitives` — leaf. Generic helpers (hasTag, tryOk/tryErr,
                      check, parseOptional, formatErrors,
                      isValidName, ordering). Takes only `lib`.
    2. `probe`      — depends on primitives. Probe value type.
    3. `member`     — depends on primitives. Member value type
                      (labelled WAN reference within a group).
    4. `wan`        — depends on primitives and probe (family-coupling
                      cross-checks against probe.families).
*/
{ lib, libnet }:
let
  primitives = import ./primitives.nix { inherit lib; };

  probe = import ./probe.nix {
    inherit lib libnet;
    internal = { inherit primitives; };
  };

  member = import ./member.nix {
    inherit lib libnet;
    internal = { inherit primitives; };
  };

  wan = import ./wan.nix {
    inherit lib libnet;
    internal = { inherit primitives probe; };
  };
in
{
  inherit
    primitives
    probe
    member
    wan
    ;
}
