package selector

import (
	"cmp"
	"slices"
)

// primaryBackup picks the healthy Member with the lowest priority
// (lower = preferred). Ties are broken by lexicographic WAN name
// so the output is deterministic. When no Member is healthy,
// Active.Has is false.
//
// Weight is ignored by this strategy — it matters once multi-active
// (load-balance) lands in v2 and the selector returns a weighted
// set rather than a single Active.
func primaryBackup(g Group, members []MemberHealth) Selection {
	healthy := healthyMembers(members)
	if len(healthy) == 0 {
		return Selection{Group: g.Name, Active: NoActive}
	}

	slices.SortFunc(healthy, func(a, b MemberHealth) int {
		if c := cmp.Compare(a.Member.Priority, b.Member.Priority); c != 0 {
			return c
		}
		return cmp.Compare(a.Member.Wan, b.Member.Wan)
	})

	return Selection{Group: g.Name, Active: Active{Wan: healthy[0].Member.Wan, Has: true}}
}

// healthyMembers filters to the subset whose Healthy is true.
// Allocates a new slice; caller's input is left intact.
func healthyMembers(members []MemberHealth) []MemberHealth {
	out := make([]MemberHealth, 0, len(members))
	for _, m := range members {
		if m.Healthy {
			out = append(out, m)
		}
	}
	return out
}
