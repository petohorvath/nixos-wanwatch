# The 0.x stability promise

From v0.1.0, every 0.x release keeps four runtime contracts compatible: `config.json` schema 1 ([daemon-config](../specs/daemon-config.md)), `state.json` schema 1 and the Hook variables ([daemon-state](../specs/daemon-state.md)), and the metric catalog ([metrics](../metrics.md)). They are frozen: no field, variable, metric, or label is removed or renamed, and none changes its type or meaning. A release may add to them: a `config.json` field that the daemon accepts as absent, a State field, a Hook variable, a metric, or a new value of an existing label. An incompatible change needs a new ADR that supersedes this one; it bumps the schema number of the `config.json` or `state.json` it changes, and ships with a migration note in [`CHANGELOG.md`](../../CHANGELOG.md). The Hook variables and the metrics carry no version number, so in 0.x they only grow.

The `services.wanwatch` options and `lib` may still break in a 0.x minor release, and [`CHANGELOG.md`](../../CHANGELOG.md) gives each break a migration note. In `lib`, four names are stable:

- `types.fwmark` and `types.routingTableId`: the option types of a netfilter fwmark and a routing-table ID, through which other modules declare options compatible with wanwatch's marks and tables. For a type, stable means that its name stays and the set of values it accepts never shrinks in 0.x, so both keep accepting `[1000, 32767]`. The set may grow, and the description, merge behaviour, and implementation may change.
- `config.paths`: the service's fixed paths ([ADR-0014](./0014-module-owns-identity-and-paths.md)), through which other modules reach `state.json`, the metrics socket, and the Hook directory, as the Telegraf module does.
- `version`: the library's version string.

Every other `lib` name is an unstable internal of the module: the value types (`wan`, `probe`, `member`, `group`, and `global`), `selector`, the rest of `config`, and the rest of `types`. The value types, `selector`, and `config` exist to validate options and render `config.json`, so their interfaces follow the module's. The rest of `types` are the module's option types, such as the `wan` and `group` submodules and the `groupStrategy` enum, or the aliases and primitives they are built from, so any option change changes them; `probeTarget` and `wanInterface` re-export nix-libnet types, so a nix-libnet update can change them.

The split follows where a break surfaces. Dashboards, alert rules, Hook scripts, and monitoring agents read the runtime contracts outside Nix evaluation, so a break there passes `nixos-rebuild` and shows up later as a wrong graph, a silent alert, or a Hook that misbehaves during a failover. A broken option or `lib` name fails evaluation with an error before anything runs. Keeping the options and `lib` open lets the module change shape in 0.x, as the declarative Hooks option and the other work in [`TODO.md`](../../TODO.md) may need.

`state.json` readers ignore fields they do not know, so an added field keeps schema 1. The earlier policy bumped the schema on every added field, so any new field would have broken every reader that checks `schema == 1`, even though the fields that reader knows kept their meaning.

`config.json` has one reader, the daemon of the same release. The renderer and the daemon ship together, and the daemon rejects unknown fields, so any change to the rendered shape, even an added optional field, needs a daemon of the same release. Its schema number guards a hand-written `config.json` and a mismatched daemon.

## Considered options

- **Freeze the options and all of `lib` as well.** The value types and the renderer are the module's implementation; freezing them would hold back every change to the module until 1.0, while a break there already fails loudly at evaluation.
- **Bump the `state.json` schema on every added field.** The schema number would then change on additions that break no reader, and the readers that check it would break for nothing.

## Consequences

The additions deferred past v0.1.0, such as a `startup` value for the Decision `reason` label, a counter for Hook events dropped from a full queue, and a `WANWATCH_SCHEMA` Hook variable, can ship in any 0.x release. Removing, renaming, or redefining anything in the four contracts needs a decision that supersedes this one. The `flake-outputs` integration suite asserts that the stable `lib` names exist, naming `types.fwmark` and `types.routingTableId` individually, and that both types accept `1000` and `32767`.
