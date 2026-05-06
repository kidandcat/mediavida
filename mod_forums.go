package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ModForumsStore tracks which subforums each MV user wants to monitor for mod
// alerts. Each entry is per mv_username so the same setup auto-routes via the
// existing per-username Telegram chat registry.
type ModForumsStore struct {
	mu     sync.RWMutex
	forums map[string][]string // mv_username → slug list (sorted, deduped)
}

func NewModForumsStore() *ModForumsStore {
	s := &ModForumsStore{forums: make(map[string][]string)}
	s.load()
	return s
}

func modForumsFile() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "mediavida-mcp", "mod_forums.json")
}

func (s *ModForumsStore) load() {
	data, err := os.ReadFile(modForumsFile())
	if err != nil {
		return
	}
	var m map[string][]string
	if err := json.Unmarshal(data, &m); err != nil {
		log.Printf("[mod-forums] decode failed: %v — starting empty", err)
		return
	}
	s.mu.Lock()
	s.forums = m
	s.mu.Unlock()
	log.Printf("[mod-forums] restored config for %d user(s) from disk", len(m))
}

func (s *ModForumsStore) save() {
	s.mu.RLock()
	data, _ := json.Marshal(s.forums)
	s.mu.RUnlock()

	path := modForumsFile()
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[mod-forums] save failed: %v", err)
	}
}

// Get returns a copy of the slug list for a user.
func (s *ModForumsStore) Get(username string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.forums[username]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// Add inserts slug into the user's list if not already present. Returns true
// if the list changed.
func (s *ModForumsStore) Add(username, slug string) bool {
	s.mu.Lock()
	for _, existing := range s.forums[username] {
		if existing == slug {
			s.mu.Unlock()
			return false
		}
	}
	list := append(s.forums[username], slug)
	sort.Strings(list)
	s.forums[username] = list
	s.mu.Unlock()
	go s.save()
	return true
}

// Remove deletes slug from the user's list. Returns true if the list changed.
func (s *ModForumsStore) Remove(username, slug string) bool {
	s.mu.Lock()
	list := s.forums[username]
	out := list[:0:0]
	changed := false
	for _, existing := range list {
		if existing == slug {
			changed = true
			continue
		}
		out = append(out, existing)
	}
	if changed {
		if len(out) == 0 {
			delete(s.forums, username)
		} else {
			s.forums[username] = out
		}
	}
	s.mu.Unlock()
	if changed {
		go s.save()
	}
	return changed
}
