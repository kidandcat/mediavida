package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenStoreAdoptsColmenaDB proves schema/data compatibility: OpenStore must
// open an mv.db that was CREATED BY the old colmena node (a copy of the real
// dev file in ./colmena-data), run its migrations (adding the newer tables via
// CREATE TABLE IF NOT EXISTS), and read the pre-existing session rows intact.
func TestOpenStoreAdoptsColmenaDB(t *testing.T) {
	src, err := os.Open("colmena-data/mv.db")
	if err != nil {
		t.Skipf("no colmena-created fixture available: %v", err)
	}
	defer src.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "mv.db")
	dst, err := os.Create(path)
	if err != nil {
		t.Fatalf("create copy: %v", err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := dst.Close(); err != nil {
		t.Fatalf("close copy: %v", err)
	}

	t.Setenv("MV_DB", path)
	s := OpenStore()
	defer s.Close()

	recs, err := s.AllSessions()
	if err != nil {
		t.Fatalf("AllSessions: %v", err)
	}
	if len(recs) == 0 {
		t.Fatalf("expected pre-existing session rows in the colmena-created mv.db, got none")
	}
	for _, r := range recs {
		if r.ClientID == "" || r.User == "" || r.Cookies == "" {
			t.Errorf("session row incomplete after adoption: %+v", r)
		}
		got, ok, gerr := s.GetSession(r.ClientID)
		if gerr != nil || !ok {
			t.Fatalf("GetSession(%s): ok=%v err=%v", r.ClientID, ok, gerr)
		}
		if got.Cookies != r.Cookies || got.User != r.User {
			t.Errorf("GetSession mismatch for %s", r.ClientID)
		}
	}
	t.Logf("adopted colmena mv.db: %d session(s) readable", len(recs))

	// The newer tables (pending_notifs, app_aliases) did not exist in the old
	// file; migrate() must have added them.
	if _, err := s.CountPendingNotifs("x"); err != nil {
		t.Errorf("pending_notifs not usable after adoption: %v", err)
	}
	if _, _, err := s.GetAppAliasOwner("x"); err != nil {
		t.Errorf("app_aliases not usable after adoption: %v", err)
	}
}
