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
    5. `group`      — depends on primitives and member. Group value
                      type — composes Members under a Strategy.
    6. `selector`   — depends on member. Pure decision logic; mirror
                      of `daemon/internal/selector`.
    7. `allocator`  — leaf. Hash+probe int allocator.
    8. `marks` / `tables` — depend on allocator. Thin wrappers.
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

  group = import ./group.nix {
    inherit lib libnet;
    internal = { inherit primitives member; };
  };

  selector = import ./selector.nix {
    inherit lib libnet;
    internal = { inherit member; };
  };

  allocator = import ./allocator.nix {
    inherit lib libnet;
    internal = { inherit primitives; };
  };

  marks = import ./marks.nix {
    inherit lib libnet;
    internal = { inherit allocator; };
  };

  tables = import ./tables.nix {
    inherit lib libnet;
    internal = { inherit allocator; };
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
    allocator
    marks
    tables
    ;
}
