# nixos-wanwatch

Multi-WAN monitoring and single-active failover for NixOS: probe each WAN, derive its Health, choose one Member per Group, and steer that Group's traffic to it.

## Monitoring

**WAN**:
An egress interface together with the Probe that tests it; the atomic monitored unit.
_Avoid_: uplink, link, gateway

**Family**:
An IP family (`v4` or `v6`) that a WAN serves, determined by the address families of its Targets.
_Avoid_: stack, protocol

**Probe**:
The configuration of how a WAN is tested: Targets, interval, thresholds, and Hysteresis.
_Avoid_: check, ping (for the configuration)

**Target**:
One IP address a Probe sends to.
_Avoid_: host, destination

**Sample**:
One probe attempt and its result, either an RTT or a loss.
_Avoid_: probe, ping (for one attempt)

**Window**:
The sliding collection of recent Samples from which RTT, jitter, and loss are computed.
_Avoid_: buffer, history

**Health**:
The derived status of a WAN, healthy or unhealthy, combined from the Health of each of its Families.
_Avoid_: status, state, up/down

**Hysteresis**:
The requirement that a threshold result persist for consecutive cycles before Health flips, in either direction.
_Avoid_: debounce, damping, Window

## Selection

**Group**:
An ordered set of Members with a Strategy, a mark, and a routing table; the unit of failover.
_Avoid_: pool, policy

**Member**:
A WAN's membership in one Group, carrying per-Group attributes such as priority and weight.
_Avoid_: WAN (when the per-Group attributes matter)

**Strategy**:
The rule that chooses the active Member from a Group's healthy Members.
_Avoid_: policy, algorithm, mode

**Selection**:
The Member a Group currently routes through, or none.
_Avoid_: active WAN, choice, Decision

**Decision**:
A change of a Group's Selection from one value to another.
_Avoid_: switch event, Selection

## Effects

**Gateway**:
The default-route next-hop of a WAN for one Family, as observed in the kernel.
_Avoid_: router, next-hop (as a configured value)

**Apply**:
Mutating kernel routing and conntrack state to carry out a Decision.
_Avoid_: commit, sync, Decision

**State**:
The published view of every WAN's Health and every Group's Selection.
_Avoid_: status file, snapshot

**Hook**:
A user script run after a Decision.
_Avoid_: callback, handler, Apply

## Relationships

- A **WAN** is a **Member** of zero or more **Groups**.
- A **Probe**, its **Health**, and its **Hysteresis** belong to the **WAN**, so a **WAN** turns unhealthy in every **Group** at once.
- A **Group** has one **Strategy** and exactly one **Selection** at a time.
- Each **Decision** triggers one **Apply**, followed by the **Hooks**.
- A **Gateway** is tracked per (**WAN**, **Family**) and independently of **Health**: a healthy **WAN** may not yet have a **Gateway**.
