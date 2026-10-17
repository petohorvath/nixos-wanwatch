package main

import "github.com/petohorvath/nixos-wanwatch/daemon/internal/selector"

// groupContainsWAN reports whether `wan` is a member of `g`.
func groupContainsWAN(g selector.Group, wan string) bool {
	_, ok := g.Members[wan]
	return ok
}

// decisionReason names the trigger for a Decision; emitted as a
// metric label and in hook env vars.
type decisionReason string

const (
	reasonHealth  decisionReason = "health"
	reasonCarrier decisionReason = "carrier"
)
