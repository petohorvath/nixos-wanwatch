# Probe algorithm (frozen spec)

The daemon runs one Pinger goroutine per (WAN, family) tuple. Each cycle sends one ICMP echo to every Target, waits up to `timeoutMs`, and pushes a Sample per Target into a sliding window. After every cycle the pinger emits a `ProbeResult` aggregating the window across all Targets.

## Per-cycle pseudocode

```
on every tick (intervalMs):
    for each target in Targets:
        seq := next sequence (per-Pinger uint16, monotonic mod 2^16)
        sendEcho(target, ident=identForWanFamily, seq=seq)
        record (seq → target, send-time)
    deadline := now + timeoutMs
    setReadDeadline(deadline)
    while replies pending:
        pkt := receive (blocks until deadline)
        if pkt is timeout: break
        (ident', seq') := parseReply(pkt)
        if ident' != ident: ignore (someone else's reply)
        if seq' not in pending: ignore (late reply from a previous cycle)
        rtt := now - send-time of seq'
        windows[target].Push(Sample{RTTMicros: rtt})
        remove seq' from pending
    for each remaining target in pending:
        windows[target].Push(Sample{Lost: true})
    emit ProbeResult{stats: Aggregate(windows)}
```

Source: `daemon/internal/probe/pinger.go:cycle`.

## ICMP identifier allocation

Each Pinger socket gets a stable 16-bit identifier — `AllocateIdents` derives it from `SHA-256(wan + "|" + family.String())[:2]` and resolves hash collisions by linear probe.

Properties:

- **Stable across daemon restarts**: same config → same identifier assignment. Useful for `tcpdump` traces.
- **Refuses to start on space exhaustion**: more than 65,536 (WAN, family) keys returns an error rather than silently reusing an identifier.
- **Order-independent of duplicates**: an exact-duplicate `IdentKey` is rejected, not silently merged.

Replies whose `ident` doesn't match the Pinger's are silently dropped — they belong to another (WAN, family) tuple's socket.

## Sequence numbers

Per-Pinger `uint16`, monotonically incrementing across cycles. Wraps at `2^16` (every 65,536 cycles). The Pinger maintains a `sent[seq] → (target, sendTime)` map per cycle and drops replies whose seq isn't currently pending.

A reply that arrives *after* its cycle's deadline is ignored — `seq` isn't in the new cycle's pending map. The Sample for that target was already marked Lost.

## Wire format

ICMPv4 (RFC 792):

```
+--------+--------+----------------+
| type=8 | code=0 | checksum       |
+--------+--------+----------------+
| ident (16 bits) | seq (16 bits)  |
+-----------------+----------------+
| payload (optional, 0+ bytes)     |
+----------------------------------+
```

ICMPv6 (RFC 4443) is identical except `type = 128`. Echo replies use `type = 0` (v4) and `type = 129` (v6).

`daemon/internal/probe/icmp.go:EchoRequestBytes` builds the request:

| Family | Checksum slot | Notes |
|---|---|---|
| v4 | Computed by user (RFC 1071 one's-complement) | Daemon fills in. |
| v6 | Computed by kernel on send | Daemon leaves zero — the pseudo-header is unavailable from `SOCK_DGRAM`. |

## Socket setup

```go
pc := net.ListenPacket("ip4:icmp", "")    // or "ip6:ipv6-icmp"
ipConn := pc.(*net.IPConn)
unix.SetsockoptString(fd, SOL_SOCKET, SO_BINDTODEVICE, wanIface)
```

`SO_BINDTODEVICE` is critical — without it a probe from `backup` could leak via `primary` and report `primary`'s health, defeating the per-WAN test. The bind affects both send (forces egress device) and receive (only accepts packets from that device).

Required capability: `CAP_NET_RAW`. The NixOS module grants it via `AmbientCapabilities`. EPERM on the bind surfaces with an explicit "need CAP_NET_RAW" hint.

## Window statistics

`WindowStats` is a fixed-capacity ring buffer over `Sample` values. Each Target has its own window.

| Stat | Computation | Boundary cases |
|---|---|---|
| `LossRatio` | `lost / total` | `0` when window empty. |
| `MeanRTT` | mean over non-Lost samples | `0` when no non-Lost samples. |
| `JitterMicros` | population stddev over non-Lost samples | `0` when fewer than two non-Lost samples. |

The window is per-Target, not per-(WAN, family). Aggregation across Targets happens in `Aggregate(targets) → FamilyStats` via an unweighted mean:

```go
FamilyStats{
    RTTMicros:    mean(t.RTTMicros    for t in nonEmptyTargets),
    JitterMicros: mean(t.JitterMicros for t in nonEmptyTargets),
    LossRatio:    mean(t.LossRatio    for t in nonEmptyTargets),
    WindowFilled: every target's window has wrapped at least once,
    PerTarget:    all targets (including empty),
}
```

Targets with empty windows still appear in `PerTarget` (as zeros) so the Prometheus label set stays stable through daemon startup — Prometheus dislikes labels that appear and disappear.

`WindowFilled` is the signal the daemon's cold-start gate keys on (PLAN §8): hysteresis only seeds once *every* per-target window is full, so a Lost first Sample (the probe loop fires before the route to the target has converged) doesn't drag the seed verdict unhealthy and produce a spurious down→up Decision pair when probes catch up.

## What this is NOT

- **Not a TCP probe.** v1 ships ICMP only. PLAN §12 OQ #2 reserves the `method` enum for `tcp` / `http` in a later version.
- **Not adaptive.** Interval and timeout are fixed per-WAN. No backoff on loss.
- **Not jitter-stabilized.** Probes fire on the configured `intervalMs` tick; no phase randomization across Pingers. Two WANs with the same interval will probe in lockstep.
- **Not a circuit breaker.** A WAN that's been unhealthy for hours still gets probed every cycle.

These are intentional simplifications for v1. The probe loop is bounded work per cycle (one packet out, one in, per target) and the cost of "wasted" probes is one ICMP packet per `intervalMs` per WAN per family.

## Threshold layer

The probe layer produces stats; the threshold + hysteresis layer turns them into a Healthy boolean. See [`docs/selector.md`](../selector.md) for that mapping.
