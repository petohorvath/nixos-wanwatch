# Prior art

`nixos-wanwatch` adds no new failover mechanism. It packages proven ones as a typed Nix library, a NixOS module with read-only outputs for downstream composition, and a single Go daemon with vendored dependencies and a hermetic three-tier test suite. The goal is to make the routing-table dance declarative, without shell glue between firewall, routing daemon, and observability stack.

| Project | Adopted | Rejected |
|---|---|---|
| [dpinger](https://github.com/dennypage/dpinger) (pfSense, OPNsense) | Fixed-N sample Window with RTT mean, stddev, and loss fraction; per-socket identifiers to demultiplex concurrent Probes. See [`probe-algorithm.md`](./probe-algorithm.md). | Single-Target probing, which lets one upstream outage shadow a healthy uplink; C and BSD `pf` coupling. |
| [mwan3](https://openwrt.org/docs/guide-user/network/wan/multiwan/mwan3) (OpenWrt) | fwmark-to-routing-table dispatch; per-Group tables shared across Families; a WAN as an uplink path rather than a bare interface. | Shell health loops around `ping`; UCI and network-reload integration; per-route metric ordering instead of an explicit selector. |
| iproute2 + iptables fwmark recipes | The mechanism: `ip rule fwmark <m> table <t>` plus a per-table default route. | Typing the same integer in two places, replaced by published values ([ADR 0007](../adr/0007-nftzones-integration-via-published-values.md)); iptables, since NixOS defaults to nftables. |
| netifd / hotplug.d (OpenWrt) | `run-parts` script directories with structured env vars, mirrored by the Hooks in [`daemon-state.md`](./daemon-state.md#hook-env-var-contract). | Link-layer triggers; Hooks fire on Decisions instead. |
| [keepalived](https://www.keepalived.org/) | Health as a state machine over observations, applied per WAN as Hysteresis. | VRRP (cross-host IP failover is a different problem); its config format. |
| Ansible and SaltStack failover modules | Nothing. | Polling the routing table for drift; the daemon is event-driven (rtnetlink and probe channels). |
| VyOS WAN load balancing | Mark once per flow, implicit in nftzones' `sroute` and `droute` placement. | Per-flow ECMP hashing, deferred to the v2 `load-balance` Strategy. |
| Linux multipath routes | Kept as the basis for v2 `load-balance`. | Use in v1: ECMP combined with conntrack and PMTUD adds failure modes ([ADR 0001](../adr/0001-single-active-failover.md)). |
