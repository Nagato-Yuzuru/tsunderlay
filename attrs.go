//go:build darwin

package main

// Log attribute keys that OpenTelemetry semantic conventions do not define.
// A key is a contract with the queries of the operator: do not rename one.
const (
	attrTrigger     = "tsunderlay.trigger"
	attrLAN         = "tsunderlay.lan"
	attrVPN         = "tsunderlay.vpn"
	attrControl     = "tsunderlay.control.prefixes"
	attrControlURLs = "tsunderlay.control.urls"
	attrDNSError    = "tsunderlay.control.dns_error"
	attrActions     = "tsunderlay.actions"
	attrBlocked     = "tsunderlay.blocked"
	attrNoEffect    = "tsunderlay.plan.no_effect"
	attrDurationMS  = "tsunderlay.duration_ms"
	attrExitReason  = "tsunderlay.exit_reason"
)

// Values of attrTrigger.
const (
	triggerStart      = "start"
	triggerRouteEvent = "route-event"
	triggerBackstop   = "backstop"
)

// Values of error.type.
const (
	errTypeRIB   = "rib"
	errTypeRoute = "route"
)
