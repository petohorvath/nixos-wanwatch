package selector

import "sort"

// primaryBackup picks the healthy Member with the lowest priority
// (lower = preferred). Ties are broken by lexicographic WAN name
// so the output is deterministic. When no Member is healthy,
// Active is nil.
//
// Weight is ignored by this strategy — it matters once multi-active
// (load-balance) lands in v2 and the selector returns a weighted
// set rather than a single Active.
func primaryBackup(g Group, members []MemberHealth) Selection {
	healthy := healthyMembers(members)
	if len(healthy) == 0 {
		return Selection{Group: g.Name, Active: nil}
	}

	sort.Slice(healthy, func(i, j int) bool {
		if healthy[i].Member.Priority != healthy[j].Member.Priority {
			return healthy[i].Member.Priority < healthy[j].Member.Priority
		}
		return healthy[i].Member.Wan < healthy[j].Member.Wan
	})

	wan := healthy[0].Member.Wan
	return Selection{Group: g.Name, Active: &wan}
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
