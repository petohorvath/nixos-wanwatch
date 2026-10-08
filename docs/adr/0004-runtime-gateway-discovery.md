# Gateways discovered at runtime, not declared

The daemon reads each WAN's Gateway, per Family, from the default routes in the kernel's main routing table over rtnetlink; the Nix configuration declares none. This replaced the `wans.<name>.gateways` option, which had to be kept in sync with the Probe's Targets and could not follow next-hops that the kernel learns at runtime. The Families a WAN serves now come solely from the address families of its Targets. Links with no broadcast next-hop (PPP, WireGuard, GRE, tun) set `pointToPoint = true` and get a `scope link` default route instead.

## Consequences

A WAN can be healthy before its Gateway is known. Apply skips the missing Families and writes their routes once the Gateway appears.
