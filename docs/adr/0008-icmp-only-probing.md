# ICMP-only probing on raw sockets

v1 probes only with ICMP and ICMPv6 echo, using a dpinger-style sliding Window of Samples. TCP, HTTP, and DNS probes are deferred. Each probe socket is a raw socket bound to the WAN's interface with `SO_BINDTODEVICE`, so a Sample measures the WAN under test rather than whichever interface the kernel would choose.

## Considered options

An unprivileged `SOCK_DGRAM` ICMP path, gated by `net.ipv4.ping_group_range`, was rejected for now. The daemon still needs `CAP_NET_ADMIN` for routes, rules, and conntrack, and `SO_BINDTODEVICE` has capability requirements of its own, so dropping `CAP_NET_RAW` would gain little. `TODO.md` records the conditions under which to revisit it.
