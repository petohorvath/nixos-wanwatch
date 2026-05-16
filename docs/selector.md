# Selector

The selector maps per-WAN Health to a per-Group Selection. It is pure and deterministic: same inputs always produce the same output. Two parallel implementations exist — `lib/internal/selector.nix` (pure Nix) and `daemon/internal/selector/` (Go) — and a cross-language drift test pins their strategy registries against each other.

## Inputs

```go
type Group struct {
    Name     string
    Strategy string
    Table    int
    Mark     int
    Members  []Member
}

type Member struct {
    Wan      string
    Weight   int     // reserved for v2 multi-active
    Priority int     // lower is preferred
}

type MemberHealth struct {
    Member  Member
    Healthy bool
}
```

`Healthy` is the *externally-visible* verdict, post-threshold and post-hysteresis. The selector never sees raw probe stats.

## Output

```go
type Selection struct {
    Group  string
    Active Active
}

// Comparable (Go `==` works between Actives), removing the
// `*string` nil-check + deref pattern at every consumer.
type Active struct {
    Wan string
    Has bool
}

// NoActive is the zero-value Active; exported for readability at
// call sites that want to name the "no member healthy" case.
var NoActive = Active{}
```

When `Active.Has` is false (`Active == NoActive`), the daemon writes no default route into the group's table. The fwmark policy rule stays in place — userspace traffic gets marked but has no next hop until a member recovers.

## Strategy: `primary-backup`

```
healthy = members where MemberHealth.Healthy
if healthy is empty:
    return Selection{Active: NoActive}
sort healthy by (priority asc, wan asc)
return Selection{Active: Active{Wan: healthy[0].Member.Wan, Has: true}}
```

Lowest `priority` wins. Ties broken by lexicographic WAN name — guarantees determinism even when two healthy members share the same priority. `weight` is ignored entirely.

### Examples

| Members (priority, healthy) | Active |
|---|---|
| `[primary=1 ✓, backup=2 ✓]` | `primary` |
| `[primary=1 ✗, backup=2 ✓]` | `backup` |
| `[primary=1 ✗, backup=2 ✗]` | `NoActive` |
| `[a=1 ✓, b=1 ✓, c=1 ✓]` | `a` (tie, lex name) |
| `[primary=2 ✗, backup=2 ✓, fallback=3 ✓]` | `backup` (lower priority among healthy) |

## Strategy registry

```go
var strategies = map[string]Strategy{
    "primary-backup": primaryBackup,
}
```

`group.validStrategies` (Nix) and `selector.KnownStrategies()` (Go) are the two surfaces that name the registry. A test under `tests/unit/internal/selector.nix` (`testStrategiesMatchGroupValidStrategies`) asserts both produce the same set — adding a strategy on one side without the other fails at eval time, not at first `selector.Select` call.

v2 will add `load-balance` once multi-active lands.

## Hysteresis

The selector treats Healthy as a boolean. Producing that boolean from raw probe samples involves two stages:

### Stage 1 — band-pass thresholds (per family)

| State | Flip-down rule | Flip-up rule |
|---|---|---|
| Healthy | `loss ≥ lossPctDown` OR `rtt ≥ rttMsDown` | (stay) |
| Unhealthy | (stay) | `loss ≤ lossPctUp` AND `rtt ≤ rttMsUp` |

Between the bands the verdict holds. The Nix-side option-type validator enforces `Up < Down` for both metrics so the band is always non-empty.

### Stage 2 — consecutive-cycle filter

A `HysteresisState` per (WAN, family) counts consecutive observations in the new direction. The verdict flips only after `consecutiveUp` (or `consecutiveDown`) successive samples cross the threshold the same way.

```go
type HysteresisState struct {
    healthyCount   int
    unhealthyCount int
    healthy        bool   // externally visible
}

func (h *HysteresisState) Observe(observed bool, up, down int) bool {
    if observed {
        h.unhealthyCount = 0
        h.healthyCount++
        if !h.healthy && h.healthyCount >= up {
            h.healthy = true
        }
    } else {
        h.healthyCount = 0
        h.unhealthyCount++
        if h.healthy && h.unhealthyCount >= down {
            h.healthy = false
        }
    }
    return h.healthy
}
```

### Cold-start path

Until the first *full* probe Window cooks for a family, that family's `familyState.cooked` flag is `false`. `combineFamilies` treats an uncooked family as a healthy vote — `carrier=up` alone is enough to mark the WAN healthy and fire an initial Decision. Once a full Window lands (`FamilyStats.WindowFilled = true`), the hysteresis-gated verdict takes over via `Hysteresis.Seed` and subsequent `ProbeResult`s feed `Hysteresis.Observe`.

The "first *full* Window" qualifier matters: emitting on every probe Sample (as the daemon used to) lets a Lost first Sample seed the hysteresis unhealthy, so a healthy WAN flaps down→up once probes converge — a spurious Decision pair PLAN §8 expressly wants to avoid. Waiting for a filled Window means we evaluate a stable verdict, not a one-off transient.

This honors PLAN §8: "health is unknown but carrier is at least known". Without it, a freshly-booted daemon would publish no Selection until probes finished accumulating samples.

### Carrier fast-track

A carrier-down event flips the WAN's `carrierUp()` to false. `buildMemberHealth` ANDs that into Healthy, so the member becomes immediately unhealthy without waiting for the probe loop to time out. Recovery follows the reverse path: carrier-up flips back to `carrierUp()` and the Selection re-evaluates.

## Determinism

`selector.compute` (Nix) and `selector.Select` (Go) are pure functions over `(Group, []MemberHealth)`. The hysteresis is stateful, but its inputs are explicit — every test exercises a fresh `HysteresisState`. Replaying the same observation sequence always produces the same verdict.

The `tests/unit/internal/selector.nix:testComputeDeterministic` test pins this: same inputs across 50 calls produce identical outputs.

## Family-policy aggregation

```nix
combineFamilies(families, policy):
    probed, healthy = 0, 0
    for f in families:
        probed++
        if !f.cooked or f.healthy:
            healthy++
    if probed == 0: return false
    switch policy:
        case "any": return healthy > 0
        default:    return healthy == probed  # "all"
```

The default is `"all"` — conservative for a routing-critical decision. `"any"` is useful for dual-stack WANs where one family being temporarily reachable is enough.

## What the selector does NOT decide

- **When to fail over.** That's hysteresis (above) and `intervalMs * consecutiveDown` after a probe-driven Decision, or sub-second after a carrier event.
- **Which routes / rules to install.** The apply layer (`daemon/internal/apply/`) translates a Selection into kernel state.
- **What to tell userspace.** The state writer + hook runner do that.

The selector is the strategy layer only. Tests live next to it (`selector_test.go`, `primarybackup_test.go`, `hysteresis_test.go`, `tests/unit/internal/selector.nix`); the full Decision pipeline is tested at the cmd/wanwatchd boundary and again end-to-end in `tests/vm/`.
