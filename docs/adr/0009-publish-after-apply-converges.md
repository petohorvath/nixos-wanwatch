# Publish a Decision only after Apply converges

A Decision's State update and Hooks wait until its routes have landed in the kernel. A hard Apply failure keeps the Decision pending, and Apply retries it on the selected WAN's next probe result. Publishing first would be simpler, but State and Hooks could then report a switch the kernel never made. Hooks are best-effort notifications outside the Apply transaction: a failing Hook is logged and never retried.
