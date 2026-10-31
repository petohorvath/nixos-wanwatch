# Probe algorithm (frozen spec)

The daemon runs one Pinger goroutine per (WAN, Family). Each cycle sends one ICMP echo to every Target, waits up to `timeoutMs`, and pushes one Sample per Target into that Target's Window. After every cycle, the Pinger emits a `ProbeResult` aggregating the Windows of all Targets. See [ADR 0008](../adr/0008-icmp-only-probing.md) for why v1 probes with ICMP only.

## Per-cycle pseudocode

```text
on every tick (intervalMs):
    open the socket if none is open (see "Socket lifetime")
    if no socket is open:
        for each target in Targets: windows[target].Push(Sample{Lost: true})
        emit ProbeResult{stats: Aggregate(windows)}
        continue
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
    if any send failed: recheck the socket (see "Socket lifetime")
    emit ProbeResult{stats: Aggregate(windows)}
```

A failed send records its Sample as Lost at once. Cancellation pulls the read deadline in, so a shutdown ends a blocked read without waiting out `timeoutMs`; the pull is armed after the cycle sets its deadline, so the cycle cannot overwrite it.

Source: `daemon/internal/probe/pinger.go:run` and `cycle`.

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

| Family | Checksum                                           | Notes                                                                                                                                                                      |
| ------ | -------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| v4     | Computed by the daemon (RFC 1071 one's complement) | The daemon fills it in.                                                                                                                                                    |
| v6     | Computed by the kernel on send                     | The daemon leaves it zero; the kernel fills it on `IPPROTO_ICMPV6` raw sockets (RFC 3542 `IPV6_CHECKSUM`), since only the kernel knows the pseudo-header's source address. |

## Socket setup

```go
pc := net.ListenPacket("ip4:icmp", "")    // or "ip6:ipv6-icmp"
ipConn := pc.(*net.IPConn)
unix.SetsockoptString(fd, SOL_SOCKET, SO_BINDTODEVICE, wanIface)
```

`SO_BINDTODEVICE` is critical. Without it, a Probe from `backup` could egress via `primary` and report `primary`'s Health. The bind applies to both directions: it forces the egress device and accepts only packets from that device.

The socket requires `CAP_NET_RAW`, which the NixOS module grants through `AmbientCapabilities`. An EPERM on the bind surfaces with an explicit "need CAP_NET_RAW" hint.

## Socket lifetime

A Pinger keeps its socket open while the WAN interface keeps its ifindex, and re-dials when the interface is recreated. `SO_BINDTODEVICE` resolves the interface name to an ifindex once, at bind time. A PPP, WireGuard, LTE, or VLAN interface that is torn down and recreated gets a new ifindex, so every send on the old socket fails.

| Event                                  | Socket                                                                                                                      | Samples                       |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- | ----------------------------- |
| Interface absent when the Pinger opens | No socket; the Pinger retries the dial every `intervalMs`.                                                                  | Lost until the dial succeeds. |
| Interface down                         | Kept: the ifindex is unchanged, so a new socket would fail the same way.                                                    | Lost while sends fail.        |
| Interface recreated or removed         | Closed after the first failed send; the next tick re-dials, and keeps retrying every `intervalMs` while the name is absent. | Lost until the re-dial.       |

The send error does not distinguish these cases: a recreated interface fails sends with `ENODEV` on v4 but `ENETUNREACH` on v6, the same error as a down link. After a failed send, the Pinger instead compares the ifindex its socket is bound to, read back with `SO_BINDTOIFINDEX`, with the interface's current ifindex. It re-dials when they differ or when the lookup fails.

A dial that fails with `ENODEV` means the interface is absent and is retried. Any other dial error, such as EPERM without `CAP_NET_RAW`, stops the Pinger, which stops the daemon so that systemd restarts it.

## Window statistics

`WindowStats` is a fixed-capacity ring buffer of `Sample` values, one per Target.

| Stat           | Computation                              | Boundary case                                            |
| -------------- | ---------------------------------------- | -------------------------------------------------------- |
| `LossCounts`   | `lost` and `total` Samples, exact counts | No data, a `total` of `0`, when the Window is empty.     |
| `MeanRTT`      | Mean over non-Lost Samples               | No data without a non-Lost Sample.                       |
| `JitterMicros` | Population stddev over non-Lost Samples  | No data without a non-Lost Sample; `0` with exactly one. |

Each statistic reports whether the Window has data for it, so a Window in which every Sample was lost never reads as a perfect RTT of `0`.

`Aggregate(targets) → FamilyStats` combines the per-Target Windows with an unweighted mean over the Targets that have data for each statistic:

```go
FamilyStats{
    RTTMicros:    mean(t.RTTMicros    for t in targets with an RTT),
    JitterMicros: mean(t.JitterMicros for t in targets with a jitter),
    ExactLoss:    mean(lost / total   for t in targets with a loss), // exact rational
    LossRatio:    float64 nearest ExactLoss,
    WindowFilled: every target's window has wrapped at least once,
    PerTarget:    all targets (including those without data), sorted by Target,
}
```

The loss mean is computed as an exact rational, so it does not depend on floating-point rounding or on the order in which `Aggregate` meets the Targets. `LossRatio`, which `state.json` and the probe gauges report, comes from it by one conversion: three Targets that each lost 1 of 10 Samples report exactly `0.1`. Each `PerTarget` entry carries its Target's exact loss and nearest `LossRatio` the same way. The RTT and jitter means are integer means, and `PerTarget` is sorted by Target, so the whole `FamilyStats` is deterministic.

A statistic is nil, no data, when no Target has data for it, and in `PerTarget` when its Target has none. `state.json` reports nil as `null` and the probe gauges report it as NaN ([daemon-state](./daemon-state.md#wansnamefamiliesv4v6), [metrics catalog](../metrics.md#probe)). Targets without data still appear in `PerTarget`, so the Prometheus label set stays stable through startup.

The thresholds compare without rounding: the exact loss against `pct / 100`, and the mean RTT in integer microseconds against `ms × 1000`. A loss or RTT exactly at a threshold therefore gets the documented verdict: at `lossPctUp` or `rttMsUp` it passes the Up thresholds, and at `lossPctDown` or `rttMsDown` it fails the Down thresholds.

The thresholds treat a statistic without data as passing: a full Window in which no Sample succeeded has no RTT, and its loss of `1` decides the verdict.

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
