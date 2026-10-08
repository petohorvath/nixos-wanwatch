# Single-active failover per Group

Each Group carries its traffic over exactly one Member at a time, chosen by the `primary-backup` Strategy. Multi-active Strategies (ECMP, weighted load balancing) are deferred to keep v1's scope manageable; they need multipath routes from Apply and several active Members in State and metrics. The Nix data model already accepts Member `weight` so v1 configurations stay forward-compatible, but no Strategy reads it yet. The `load-balance` Strategy is tracked as v2 work in `TODO.md`.
