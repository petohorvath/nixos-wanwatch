# Firewall integration through published values

wanwatch and the firewall share only integers. wanwatch publishes each Group's mark and table as `services.wanwatch.marks.<group>` and `.tables.<group>`. It installs `fwmark <mark> table <table>` rules for both Families and owns the contents of each Group's table. The firewall (nftzones or plain nftables) sets the mark on the traffic it wants steered and references the published values by name. Neither project depends on the other's module, and nftzones needed no changes for v1. See [`docs/nftzones-integration.md`](../nftzones-integration.md).
