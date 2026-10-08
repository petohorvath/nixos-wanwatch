# User-declared marks and routing tables

Every Group declares its own fwmark and routing-table ID, both in `1000..32767`, and module evaluation rejects duplicates across Groups. v0.1.0 derived them automatically by hashing the set of Group names with linear probing. Because each value depended on every Group's name, adding or renaming one Group could silently shift another Group's mark and break downstream firewall rules. Explicit integers keep each value stable. The range excludes the kernel-reserved tables 253–255 and the small integers that ad-hoc scripts often use.

## Consequences

Users must pick and maintain the integers themselves. Downstream configuration still refers to them by name through `services.wanwatch.marks.<group>` and `.tables.<group>`.
