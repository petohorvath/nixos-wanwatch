# Publish a Decision only after Apply succeeds

A Decision's State update and Hooks wait until Apply succeeds. A hard Apply failure (an interface lookup or netlink write error) keeps the Decision pending, and Apply retries it on the selected WAN's next probe result. Publishing first would be simpler, but State and Hooks could then report a switch the kernel never made. Hooks are best-effort notifications outside the Apply transaction: a failing Hook is logged and never retried.

## Consequences

A Family with no known Gateway is skipped, not failed (see [ADR-0004](./0004-runtime-gateway-discovery.md)), so a Decision can be published while that Family still routes through the previous WAN. Its route is written once the Gateway appears.
