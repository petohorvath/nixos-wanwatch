/*
  internal — operational code for the wanwatch lib. Exposed under
  `wanwatch.internal.<module>`.

  Layout mirrors nftzones' three-tier internal hierarchy: leaf
  modules import nothing from sibling internals; later layers
  receive previously-built modules via `internal = …`. This file
  pipes the layers together.

  Layer order:
    1. `primitives` — leaf. Generic helpers (tryOk/tryErr,
                      check, partitionTry, formatErrors,
                      isValidName, isPositiveInt). Takes only `lib`.
    2. `probe`      — depends on primitives. Probe value type.
    3. `member`     — depends on primitives. Member value type
                      (labelled WAN reference within a group).
    4. `wan`        — depends on primitives and probe (family-coupling
                      cross-checks against probe.families).
    5. `group`      — depends on primitives and member. Group value
                      type — composes Members under a Strategy.
    6. `selector`   — depends on member. Pure decision logic; mirror
                      of `daemon/internal/selector`.
    7. `config`     — depends on wan + group. Daemon-config JSON
                      renderer with cross-group duplicate-mark/
                      table assertions.
*/
{ lib, libnet }:
let
  primitives = import ./primitives.nix { inherit lib; };

  probe = import ./probe.nix {
    inherit lib libnet;
    internal = { inherit primitives; };
  };

  member = import ./member.nix {
    internal = { inherit primitives; };
  };

  wan = import ./wan.nix {
    inherit libnet;
    internal = { inherit primitives probe; };
  };

  group = import ./group.nix {
    inherit lib;
    internal = { inherit primitives member; };
  };

  selector = import ./selector.nix {
    inherit lib;
  };

  config = import ./config.nix {
    inherit lib;
    internal = { inherit wan group; };
  };
in
{
  inherit
    primitives
    probe
    member
    wan
    group
    selector
    config
    ;
}
