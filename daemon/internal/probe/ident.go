package probe

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// IdentKey identifies a single probe socket (one per WAN+family
// tuple, per PLAN §8 internal/probe).
type IdentKey struct {
	Wan    string
	Family Family
}

// AllocateIdents picks a stable 16-bit ICMP identifier for every
// key in `keys`, with linear-probe collision resolution so two keys
// that hash to the same slot still get distinct identifiers. The
// returned map is deterministic for a given input set — re-running
// the daemon against the same config produces the same assignment,
// which keeps tcpdump traces interpretable across restarts.
//
// Returns an error if the 16-bit space is exhausted (>65535 keys) —
// the daemon refuses to start rather than silently reusing an
// identifier and demultiplexing replies to the wrong window.
func AllocateIdents(keys []IdentKey) (map[IdentKey]uint16, error) {
	if len(keys) > identSpace {
		return nil, fmt.Errorf("probe: %d keys exceeds 16-bit identifier space", len(keys))
	}
	allocated := make(map[IdentKey]uint16, len(keys))
	owners := make(map[uint16]IdentKey, len(keys))

	for _, k := range keys {
		if _, dup := allocated[k]; dup {
			return nil, fmt.Errorf("probe: duplicate IdentKey %+v", k)
		}
		start := initialIdent(k)
		assigned := false
		for offset := 0; offset < identSpace; offset++ {
			candidate := uint16((int(start) + offset) % identSpace)
			if _, taken := owners[candidate]; taken {
				continue
			}
			owners[candidate] = k
			allocated[k] = candidate
			assigned = true
			break
		}
		if !assigned {
			return nil, fmt.Errorf("probe: identifier space exhausted at key %+v", k)
		}
	}
	return allocated, nil
}

// identSpace is the size of the ICMP identifier field (16 bits).
const identSpace = 1 << 16

// initialIdent is the SHA-256-derived starting point for a key's
// linear probe. Stable across daemon restarts — same key → same
// initial ident — so a wireshark capture from yesterday still
// matches today's run when the config hasn't changed.
func initialIdent(k IdentKey) uint16 {
	h := sha256.Sum256([]byte(k.Wan + "|" + k.Family.String()))
	return binary.BigEndian.Uint16(h[:2])
}
