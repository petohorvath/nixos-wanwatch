# Probe algorithm (frozen spec)

The daemon runs one Pinger goroutine per (WAN, Family). Each cycle sends one ICMP echo to every Target, waits up to `timeoutMs`, and pushes one Sample per Target into that Target's Window. After every cycle, the Pinger emits a `ProbeResult` aggregating the Windows of all Targets. See [ADR 0008](../adr/0008-icmp-only-probing.md) for why v1 probes with ICMP only.

## Per-cycle pseudocode

```text
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

Each Pinger socket gets a stable 16-bit identifier. `AllocateIdents` derives it from `SHA-256(wan + "|" + family.String())[:2]` and resolves hash collisions by linear probing. The allocation is:

- **Stable across restarts**: the same config yields the same identifiers, which keeps `tcpdump` traces comparable.
- **Fail-fast on exhaustion**: more than 65,536 (WAN, Family) keys return an error instead of reusing an identifier.
- **Strict on duplicates**: an exact-duplicate `IdentKey` is rejected, not merged.

A Pinger silently drops replies whose `ident` does not match its own; they belong to another (WAN, Family) socket.

## Sequence numbers

Each Pinger keeps a `uint16` sequence that increments across cycles and wraps at `2^16` (every 65,536 cycles). Per cycle, it maintains a `sent[seq] → (target, sendTime)` map and drops replies whose `seq` is not pending.

A reply arriving after its cycle's deadline is ignored, because its `seq` is not in the new cycle's pending map. That Target's Sample was already recorded as Lost.

## Wire format

ICMPv4 (RFC 792):

```text
+--------+--------+----------------+
| type=8 | code=0 | checksum       |
+--------+--------+----------------+
| ident (16 bits) | seq (16 bits)  |
+-----------------+----------------+
| payload (optional, 0+ bytes)     |
+----------------------------------+
```

ICMPv6 (RFC 4443) is identical except that `type = 128`. Echo replies use `type = 0` (v4) and `type = 129` (v6).

`daemon/internal/probe/icmp.go:EchoRequestBytes` builds the request:

| Family | Checksum | Notes |
|---|---|---|
| v4 | Computed by the daemon (RFC 1071 one's complement) | The daemon fills it in. |
| v6 | Computed by the kernel on send | The daemon leaves it zero; the kernel fills it on `IPPROTO_ICMPV6` raw sockets (RFC 3542 `IPV6_CHECKSUM`), since only the kernel knows the pseudo-header's source address. |

## Socket setup

```go
pc := net.ListenPacket("ip4:icmp", "")    // or "ip6:ipv6-icmp"
ipConn := pc.(*net.IPConn)
unix.SetsockoptString(fd, SOL_SOCKET, SO_BINDTODEVICE, wanIface)
```

`SO_BINDTODEVICE` is critical. Without it, a Probe from `backup` could egress via `primary` and report `primary`'s Health. The bind applies to both directions: it forces the egress device and accepts only packets from that device.

The socket requires `CAP_NET_RAW`, which the NixOS module grants through `AmbientCapabilities`. An EPERM on the bind surfaces with an explicit "need CAP_NET_RAW" hint.

## Window statistics

`WindowStats` is a fixed-capacity ring buffer of `Sample` values, one per Target.

| Stat | Computation | Boundary case |
|---|---|---|
| `LossRatio` | `lost / total` | `0` when the Window is empty. |
| `MeanRTT` | Mean over non-Lost Samples | `0` with no non-Lost Samples. |
| `JitterMicros` | Population stddev over non-Lost Samples | `0` with fewer than two non-Lost Samples. |

`Aggregate(targets) → FamilyStats` combines the per-Target Windows with an unweighted mean:

```go
FamilyStats{
    RTTMicros:    mean(t.RTTMicros    for t in nonEmptyTargets),
    JitterMicros: mean(t.JitterMicros for t in nonEmptyTargets),
    LossRatio:    mean(t.LossRatio    for t in nonEmptyTargets),
    WindowFilled: every target's window has wrapped at least once,
    PerTarget:    all targets (including empty),
}
```

Targets with empty Windows still appear in `PerTarget`, as zeros, so the Prometheus label set stays stable through startup.

The daemon's cold-start gate keys on `WindowFilled`: Hysteresis seeds only once every per-Target Window is full. Otherwise a Lost first Sample, sent before the route to the Target has converged, would seed the verdict unhealthy and cause a spurious down→up Decision pair once Probes catch up. See [Cold-start invariant](./failover.md#cold-start-invariant).

## Non-features

v1 deliberately omits these:

- **TCP probes.** v1 ships ICMP only; the `method` enum is reserved for `tcp` and `http` later.
- **Adaptivity.** Interval and timeout are fixed per WAN, with no backoff on loss.
- **Phase randomization.** Probes fire on the `intervalMs` tick, so two WANs with the same interval probe in lockstep.
- **Circuit breaking.** A WAN unhealthy for hours is still probed every cycle.

Per cycle, the probe loop does bounded work: one packet out and one in per Target. A wasted Probe costs one ICMP packet per `intervalMs` per WAN per Family.

## Threshold layer

The probe layer produces statistics; the threshold and Hysteresis layer turns them into Health. See [`docs/selector.md`](../selector.md).
