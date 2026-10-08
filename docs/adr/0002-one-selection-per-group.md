# One Selection per Group across Families

A Group has one Selection that is applied to every Family the selected Member has a Gateway for, rather than one Selection per (Group, Family). Splitting by Family would let v4 route through one Member while v6 routes through another, but it doubles the Decision state space and complicates State, Hooks, and metrics. Per-WAN Health already combines its Families through `familyHealthPolicy` (see [ADR-0005](./0005-family-health-policy-defaults-to-all.md)). Per-Family Selection is tracked as v2 work in `TODO.md`.
