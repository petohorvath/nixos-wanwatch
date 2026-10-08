# `familyHealthPolicy` defaults to `all`

A dual-stack WAN is healthy only when every Family it serves is healthy, unless the user sets `probe.familyHealthPolicy = "any"`. The conservative default steers traffic away from a partially broken WAN. `any` suits ISPs whose IPv6 is unreliable while IPv4 carries the primary path.
