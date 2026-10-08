# Selector

The selector maps per-WAN Health to a per-Group Selection. It is a pure, deterministic function implemented twice: `lib/internal/selector.nix` (Nix) and `daemon/internal/selector/` (Go). A cross-language test pins their Strategy registries to each other.

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
    Weight   int // reserved for multi-active Strategies
    Priority int // lower is preferred
}

type MemberHealth struct {
    Member  Member
    Healthy bool
}
```

`Healthy` is the Health verdict after thresholds and Hysteresis. The selector never sees raw Probe statistics.

## Output

```go
type Selection struct {
    Group  string
    Active Active
}

// Comparable with ==.
type Active struct {
    Wan string
    Has bool
}

// The "no Member healthy" value.
var NoActive = Active{}
```

When `Active == NoActive`, the daemon writes no routes for the Group. The previous default route and the fwmark rule stay in place until a Member recovers.

## Strategy: `primary-backup`

```text
healthy = members where MemberHealth.Healthy
if healthy is empty:
    return Selection{Active: NoActive}
sort healthy by (priority asc, wan asc)
return Selection{Active: Active{Wan: healthy[0].Member.Wan, Has: true}}
```

The lowest `priority` wins, and ties go to the lexicographically first WAN name, so equal priorities stay deterministic. `weight` is ignored.

| Members (priority, Health) | Active |
|---|---|
| `[primary=1 ✓, backup=2 ✓]` | `primary` |
| `[primary=1 ✗, backup=2 ✓]` | `backup` |
| `[primary=1 ✗, backup=2 ✗]` | `NoActive` |
| `[a=1 ✓, b=1 ✓, c=1 ✓]` | `a` (tie, lexicographic name) |
| `[primary=2 ✗, backup=2 ✓, fallback=3 ✓]` | `backup` (lowest healthy priority) |

## Strategy registry

```go
var strategies = map[string]Strategy{
    "primary-backup": primaryBackup,
}
```

`group.validStrategies` (Nix) and `selector.KnownStrategies()` (Go) both name the registry. `testStrategiesMatchGroupValidStrategies` in `tests/unit/internal/selector.nix` asserts they match, so adding a Strategy on one side only fails at evaluation time. A `load-balance` Strategy is deferred until multi-active Selection exists; see [ADR 0001](./adr/0001-single-active-failover.md).

## Hysteresis

Two stages turn raw Samples into the boolean Health the selector consumes.

### Stage 1: band-pass thresholds (per Family)

| Current Health | Becomes unhealthy when | Becomes healthy when |
|---|---|---|
| healthy | `loss ≥ lossPctDown` or `rtt ≥ rttMsDown` | — |
| unhealthy | — | `loss ≤ lossPctUp` and `rtt ≤ rttMsUp` |

Between the bands, Health holds. The Nix option type enforces `Up < Down` for both metrics.

### Stage 2: consecutive-cycle filter

A `HysteresisState` per (WAN, Family) counts consecutive observations in the new direction. Health flips only after `consecutiveUp` or `consecutiveDown` successive observations agree. Thresholds below 1 are clamped to 1; the Nix layer is the authoritative validator.

```go
func (h *HysteresisState) Observe(observedHealthy bool) bool {
    if observedHealthy {
        h.unhealthyCount = 0
        h.healthyCount++
        if !h.healthy && h.healthyCount >= h.consecutiveUp {
            h.healthy = true
        }
    } else {
        h.healthyCount = 0
        h.unhealthyCount++
        if h.healthy && h.unhealthyCount >= h.consecutiveDown {
            h.healthy = false
        }
    }
    return h.healthy
}
```

### Cold start

A Family stays uncooked (`familyState.cooked = false`) until its first full Window. `combineFamilies` counts an uncooked Family as healthy, so carrier-up alone makes the WAN healthy and produces an initial Decision instead of publishing no Selection while Samples accumulate.

When the Window fills (`FamilyStats.WindowFilled`), `HysteresisState.Seed` adopts the measured Health directly, and later results go through `Observe`. Waiting for a full Window prevents one lost first Sample from seeding the WAN unhealthy and producing a spurious down/up Decision pair. The cold-start invariant is specified in [`specs/failover.md`](./specs/failover.md#cold-start-invariant).

### Carrier fast-track

Carrier loss makes `carrierUp()` false, and `buildMemberHealth` ANDs it into `Healthy`, so the Member becomes unhealthy without waiting for Probe timeouts. Carrier recovery reverses the path and the Selection re-evaluates.

## Determinism

`selector.compute` (Nix) and `selector.Select` (Go) are pure functions of `(Group, []MemberHealth)`. Hysteresis is stateful but has explicit inputs, so replaying the same observation sequence yields the same Health. `testComputeDeterministic` in `tests/unit/internal/selector.nix` checks that 50 identical calls return identical output.

## Family-policy aggregation

```text
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

The default `all` is conservative for a routing decision; `any` suits dual-stack WANs where one reachable Family is enough. See [ADR 0005](./adr/0005-family-health-policy-defaults-to-all.md).

## Out of scope

- **Failover timing.** Hysteresis sets it: about `intervalMs * consecutiveDown` for a Probe-driven Decision, sub-second for a carrier event.
- **Routes and rules.** `daemon/internal/apply/` translates a Selection into kernel state.
- **Userspace notification.** The State writer and Hook runner handle it.

Selector tests live in `selector_test.go`, `primarybackup_test.go`, `hysteresis_test.go`, and `tests/unit/internal/selector.nix`. The full Decision pipeline is tested in `cmd/wanwatchd` and end to end in `tests/vm/`.
