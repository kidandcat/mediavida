package main

import (
	"log"
	"sort"
	"sync"
)

// ModForumsStore tracks which subforums each MV user wants to monitor for mod
// alerts. Each entry is per mv_username so the same setup auto-routes via the
// existing per-username Telegram chat registry. Subscriptions are persisted in
// the store (cs.AddModForum/RemoveModForum/GetModForums); the in-memory map is a
// write-through cache hydrated from the store at startup.
type ModForumsStore struct {
	cs     *Store
	mu     sync.RWMutex
	forums map[string][]string // mv_username → slug list (sorted, deduped)
}

func NewModForumsStore(cs *Store) *ModForumsStore {
	s := &ModForumsStore{cs: cs, forums: make(map[string][]string)}
	s.load()
	return s
}

func (s *ModForumsStore) load() {
	all, err := s.cs.AllModForums()
	if err != nil {
		log.Printf("[mod-forums] load failed: %v — starting empty", err)
		return
	}
	s.mu.Lock()
	for username, slugs := range all {
		list := make([]string, len(slugs))
		copy(list, slugs)
		sort.Strings(list)
		s.forums[username] = list
	}
	s.mu.Unlock()
	log.Printf("[mod-forums] restored config for %d user(s) from store", len(all))
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
	if err := s.cs.AddModForum(username, slug); err != nil {
		log.Printf("[mod-forums] persist add %s/%s failed: %v", username, slug, err)
	}
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
		if err := s.cs.RemoveModForum(username, slug); err != nil {
			log.Printf("[mod-forums] persist remove %s/%s failed: %v", username, slug, err)
		}
	}
	return changed
}
