package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

// modStateOnDisk is the JSON shape persisted to disk so the poller can resume
// across restarts without re-alerting on already-seen reports/messages.
type modStateOnDisk struct {
	Mod     map[string]map[string]*ModBubbles `json:"mod"`     // clientID → slug → counters
	Reports map[string]map[string][]string    `json:"reports"` // clientID → slug → report keys
}

func modStateFile() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "mediavida-mcp", "mod_state.json")
}

func loadModState() (map[string]map[string]*ModBubbles, map[string]map[string]map[string]struct{}) {
	mod := make(map[string]map[string]*ModBubbles)
	reports := make(map[string]map[string]map[string]struct{})

	data, err := os.ReadFile(modStateFile())
	if err != nil {
		return mod, reports
	}
	var disk modStateOnDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		log.Printf("[mod-state] decode failed: %v — starting fresh", err)
		return mod, reports
	}
	if disk.Mod != nil {
		mod = disk.Mod
	}
	for clientID, bySlug := range disk.Reports {
		reports[clientID] = make(map[string]map[string]struct{}, len(bySlug))
		for slug, keys := range bySlug {
			set := make(map[string]struct{}, len(keys))
			for _, k := range keys {
				set[k] = struct{}{}
			}
			reports[clientID][slug] = set
		}
	}
	log.Printf("[mod-state] restored state for %d client(s) from disk", len(mod))
	return mod, reports
}

// marshalModState builds a JSON snapshot of the in-memory maps. Must be called
// from the same goroutine that mutates the maps (the poll goroutine) so the
// read is race-free.
func marshalModState(mod map[string]map[string]*ModBubbles, reports map[string]map[string]map[string]struct{}) []byte {
	disk := modStateOnDisk{
		Mod:     mod,
		Reports: make(map[string]map[string][]string, len(reports)),
	}
	for clientID, bySlug := range reports {
		disk.Reports[clientID] = make(map[string][]string, len(bySlug))
		for slug, set := range bySlug {
			keys := make([]string, 0, len(set))
			for k := range set {
				keys = append(keys, k)
			}
			disk.Reports[clientID][slug] = keys
		}
	}
	data, _ := json.Marshal(disk)
	return data
}

// writeModState persists a previously-marshaled snapshot to disk. Safe to call
// in a goroutine since the snapshot is already detached from the live maps.
func writeModState(data []byte) {
	path := modStateFile()
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[mod-state] save failed: %v", err)
	}
}
