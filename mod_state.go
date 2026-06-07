package main

// Mod-tracking prev-counters (prevMod/prevReports) are EPHEMERAL change-detection
// state: they re-baseline on every restart so the poller never re-alerts on
// reports/messages already surfaced. They are intentionally NOT persisted — not
// to Colmena, not to disk. loadModState always starts empty; marshalModState and
// writeModState are no-ops kept only so the existing caller in bubbles.go
// (persistModState) compiles unchanged.

func loadModState() (map[string]map[string]*ModBubbles, map[string]map[string]map[string]struct{}) {
	mod := make(map[string]map[string]*ModBubbles)
	reports := make(map[string]map[string]map[string]struct{})
	return mod, reports
}

// marshalModState is a no-op: ephemeral mod-tracking state is never persisted.
func marshalModState(_ map[string]map[string]*ModBubbles, _ map[string]map[string]map[string]struct{}) []byte {
	return nil
}

// writeModState is a no-op: ephemeral mod-tracking state is never persisted.
func writeModState(_ []byte) {}
