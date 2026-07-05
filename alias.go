package main

import "log"

// AppAliasStore is the durable registry mapping a phone's per-install device
// token to the single MV session for its account (the owner clientID), backed
// by the durable SQLite store. It mirrors WatchTokenStore: an aliased token has NO MV session of
// its own — it resolves to the owner's session at request time (see
// APITokenMiddleware), so there is only ever one Mediavida login per account no
// matter how many phones/watches use it. Duplicating the session per device was
// what triggered the mutual-logout storm that left the app stuck on 503.
type AppAliasStore struct {
	cs *Store
}

// NewAppAliasStore builds an app-alias store backed by the durable SQLite store.
func NewAppAliasStore(cs *Store) *AppAliasStore {
	return &AppAliasStore{cs: cs}
}

// Add points a device token at an owner's session (idempotent).
func (s *AppAliasStore) Add(token, owner string) {
	if err := s.cs.AddAppAlias(token, owner); err != nil {
		log.Printf("[alias] failed to add app alias for owner %s: %v", owner, err)
	}
}

// Owner resolves a device token to its owner client ID.
func (s *AppAliasStore) Owner(token string) (string, bool) {
	owner, ok, err := s.cs.GetAppAliasOwner(token)
	if err != nil {
		log.Printf("[alias] failed to read owner for app token: %v", err)
		return "", false
	}
	return owner, ok
}

// Delete removes a device-token alias.
func (s *AppAliasStore) Delete(token string) {
	if err := s.cs.DeleteAppAlias(token); err != nil {
		log.Printf("[alias] failed to delete app alias: %v", err)
	}
}
