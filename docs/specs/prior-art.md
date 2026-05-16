# Prior art (distillation)

The planning phase surveyed existing multi-WAN failover tools. This doc distills what each does, what wanwatch took from it, and what it rejected.

## dpinger

[`dpinger`](https://github.com/dennypage/dpinger) is the sample-window pinger embedded in pfSense / OPNsense. Single-target ICMP, computes a rolling RTT / loss average, signals a state change via a configurable command.

| Adopted | The sliding-window-of-fixed-N-samples algorithm. RTT mean + stddev. Loss as a fraction of the window. |
|---|---|
| Adopted | Per-WAN identifier allocation to demux replies from concurrent probes on the same socket. |
| Rejected | Single-target probing — wanwatch probes a list to avoid one upstream's outage shadowing a healthy uplink. |
| Rejected | C codebase + libpfctl integration — wanwatch is Go + netlink, no BSD pf assumption. |

`docs/specs/probe-algorithm.md` is the closest analogue; the wire-format-only test (`icmp.go:ParseEchoReply`) mirrors dpinger's wire reader.

## mwan3

[`mwan3`](https://openwrt.org/docs/guide-user/network/wan/multiwan/mwan3) is OpenWrt's policy-routing-based multi-WAN. Marks LAN traffic by zone, dispatches to per-WAN routing tables, runs `mwan3track` shell loops that fire `mwan3rtmon` on state changes.

| Adopted | The fwmark-to-routing-table dispatch model. PLAN §6.1 is the same idea, just packaged as a NixOS option surface. |
|---|---|
| Adopted | Per-Group routing tables, shared across families. |
| Rejected | Shell-based health tracking (`mwan3track` calls `iputils-ping` in a loop). Goroutines + a real ICMP socket cost less and avoid forking. |
| Rejected | UCI / network reload integration — wanwatch composes with `services.telegraf` and `networking.nftzones`, not with OpenWrt-specific config. |
| Rejected | The "metric" knob (per-route metric ordering). wanwatch's selector decides explicitly. |

mwan3's interface model influenced PLAN §5.4's "WAN = (interface, gateways{v4,v6})" choice — a WAN is the *uplink path*, not the interface alone.

## iproute2 + iptables fwmark recipes

Hand-rolled "set up two uplinks with fwmark" guides litter mailing lists. The pattern:

```sh
ip rule add fwmark 100 table 100
ip route add default via 192.0.2.1 dev eth0 table 100
iptables -t mangle -A PREROUTING -i br-lan -j MARK --set-mark 100
```

| Adopted | The mechanism. wanwatch validates the user-declared integers, writes the rules, owns the route table. |
|---|---|
| Rejected | Re-typing the same integer in two files. wanwatch's `services.wanwatch.marks.<group>` re-exposes the user-declared int as a read-only attribute so downstream modules reference by name — no drift between firewall rule and route-installer. |
| Rejected | iptables — wanwatch composes with nftables (via nftzones or raw `networking.nftables.tables`). iptables is fine in principle; nftables is the NixOS default. |

## netifd / hotplug.d on OpenWrt

OpenWrt's `netifd` fires hotplug scripts on interface state changes. The scripts are run-parts-style: drop a shell file in `/etc/hotplug.d/iface/`, get called with the event.

| Adopted | run-parts convention. PLAN §5.5 hooks under `/etc/wanwatch/hooks/{up,down,switch}.d/*` follow the same pattern. |
|---|---|
| Adopted | Structured env vars (`INTERFACE`, `ACTION`, ...) → `WANWATCH_*`. Hooks are user-extension points; env vars are the contract. |
| Rejected | The hotplug *trigger*. Hotplug fires on link-layer events; wanwatch hooks fire on *Decisions* (the daemon's idea of "things changed enough to matter"). |

## keepalived

[`keepalived`](https://www.keepalived.org/) is the canonical VRRP daemon. State-machine driven, supports `notify` scripts on state changes.

| Adopted | The state-machine view — Health is a function over observations, not a momentary reading. wanwatch's hysteresis is essentially keepalived's `weight` mechanism applied per-WAN. |
|---|---|
| Rejected | VRRP itself. wanwatch is about per-host egress selection; VRRP is about IP failover across hosts. Different problem. |
| Rejected | The TCL-ish config format. NixOS option types are the v1 interface. |

## SaltStack / Ansible "wan failover" community modules

Many: ad-hoc Python or Bash that wraps `ping`, polls every N seconds, calls `ip route replace`. None we'd reach for.

Common pattern wanwatch rejects: **polling the routing table to detect "is the route still right?"**. wanwatch is event-driven (rtnl + probe channels into a select loop). Polling-the-kernel-for-changes is fragile and adds latency.

## VyOS WAN load balancing

VyOS bundles a multi-WAN feature comparable to mwan3 but with `connmark` for stateful per-flow assignment.

| Adopted | The per-Group-mark-applied-once-per-flow model is implicit in nftzones's `meta mark set` placement in `sroute` (forwarded traffic) vs `droute` (locally-originated). |
|---|---|
| Rejected | Per-flow ECMP / hashing — that's v2's `load-balance` strategy, deliberately deferred. |

## Linux `multipath` routes

Linux supports ECMP via `nexthop` on a single route:

```sh
ip route add default \
    nexthop via 192.0.2.1 dev eth0 weight 1 \
    nexthop via 100.64.0.1 dev wwan0 weight 2
```

| Adopted | The capability is there for v2's `load-balance`. The `apply.WriteDefault` signature will gain a `[]Member` form alongside the current single-Gateway form. |
|---|---|
| Rejected | v1 — single-active is simpler to reason about. ECMP plus conntrack plus PMTUD is a rich set of footguns we'd rather not own day one. |

## Summary

The interesting bits in v1 are not new. The novelty is the *packaging*:

- Pure-Nix typed library with per-value-type tests.
- NixOS module exposing read-only outputs for downstream composition.
- Single Go daemon, vendored deps, hermetic build, three-tier test suite.

If a tool already does the routing-table dance well, the engineering goal here is to make it declaratively expressible in a NixOS config without a hand-tuned shell glue between firewall, router daemon, and observability stack.
