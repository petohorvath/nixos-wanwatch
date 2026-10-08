# Restart-only reconfiguration

`wanwatchd` reads its configuration once at startup and does not reload on SIGHUP; configuration changes take effect on a service restart. A hot reload would have to reconcile kernel rules, routes, and in-flight Selections against a changed set of Groups, marks, and tables, and the right shape for that changes Selection and Apply semantics. Hot reload is tracked as v2 work in `TODO.md`.
