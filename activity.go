package main

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

// PendingNotifStore is a durable mirror of a device's unseen MV notifications,
// backed by the durable SQLite store. Historically it compensated for the
// poller marking avisos seen while enriching pushes; the poller no longer does
// that, so this store is only written when a client opens GET /notifications
// via the API (and cleared on that same open). Kept for badge compatibility.
type PendingNotifStore struct {
	cs *Store
}

// NewPendingNotifStore builds a pending-notification store on the durable SQLite store.
func NewPendingNotifStore(cs *Store) *PendingNotifStore {
	return &PendingNotifStore{cs: cs}
}

// Add mirrors the given unseen notifications for a client (deduped by MV id).
func (s *PendingNotifStore) Add(clientID string, items []NotificationItem) {
	if s == nil || len(items) == 0 {
		return
	}
	now := time.Now().Unix()
	for _, it := range items {
		if it.ID == "" {
			continue
		}
		if err := s.cs.AddPendingNotif(clientID, it.ID, it.URL, now); err != nil {
			log.Printf("[pending] add failed for %s: %v", clientID, err)
			return
		}
	}
}

// Count returns how many unseen notifications are mirrored for a client (0 on error).
func (s *PendingNotifStore) Count(clientID string) int {
	if s == nil {
		return 0
	}
	n, err := s.cs.CountPendingNotifs(clientID)
	if err != nil {
		log.Printf("[pending] count failed for %s: %v", clientID, err)
		return 0
	}
	return n
}

// CountByThread groups the mirrored notifications by thread key (see threadKey).
func (s *PendingNotifStore) CountByThread(clientID string) map[string]int {
	out := map[string]int{}
	if s == nil {
		return out
	}
	urls, err := s.cs.PendingNotifURLs(clientID)
	if err != nil {
		log.Printf("[pending] urls failed for %s: %v", clientID, err)
		return out
	}
	for _, u := range urls {
		out[threadKey(u)]++
	}
	return out
}

// Clear drops the mirrored notifications for a client (app opened the feed).
func (s *PendingNotifStore) Clear(clientID string) {
	if s == nil {
		return
	}
	if err := s.cs.ClearPendingNotifs(clientID); err != nil {
		log.Printf("[pending] clear failed for %s: %v", clientID, err)
	}
}

// ThreadActivity is a favorite thread with new unread posts, enriched from the
// favorites page so the push can name the thread and show its unread count.
type ThreadActivity struct {
	Title string // thread title
	URL   string // canonical thread URL (deep link target)
	Key   string // stable per-thread grouping key (see threadKey)
	Count int    // unread posts in the thread (0 = unknown / "new")
}

// MentionActivity is a mention/quote ("aviso"), grouped per thread. Count is the
// cumulative number of unseen avisos in that thread (from the pending store), so
// the per-thread notification can be updated in place as more arrive.
type MentionActivity struct {
	Key    string // stable per-thread grouping key (see threadKey)
	Author string // who mentioned, from the newest aviso in the thread
	Text   string // full phrasing of the newest aviso ("X te ha citado en …")
	Target string // thread title
	URL    string // deep link to the aviso's post
	Count  int    // cumulative unseen avisos in this thread
}

// threadKey reduces any MV thread/post URL to a short stable identifier (the
// thread id, e.g. "t123456"), used as the FCM notification tag / apns-collapse-id
// so repeated pushes about the same thread replace one another. Page numbers and
// post anchors are stripped. Falls back to the last path segment, then the URL.
func threadKey(u string) string {
	if u == "" {
		return ""
	}
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	u = strings.TrimRight(u, "/")
	segs := strings.Split(u, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		s := segs[i]
		if s == "" {
			continue
		}
		// A bare page-number segment (".../slug-123/45") → skip past it.
		if _, err := strconv.Atoi(s); err == nil {
			continue
		}
		// The slug carries the thread id as its trailing "-<digits>".
		if j := strings.LastIndexByte(s, '-'); j >= 0 {
			if tid := s[j+1:]; tid != "" {
				if _, err := strconv.Atoi(tid); err == nil {
					return "t" + tid
				}
			}
		}
		return s
	}
	return u
}

// parseUnread turns a favorites unread badge ("3", "new", "") into a count.
// 0 means "unknown / just new" (the badge had no explicit number).
func parseUnread(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// favBody renders the body for a favorite-thread push.
func favBody(count int, title string) string {
	switch {
	case count == 1:
		return fmt.Sprintf("1 mensaje nuevo en %s", title)
	case count > 1:
		return fmt.Sprintf("%d mensajes nuevos en %s", count, title)
	default:
		return fmt.Sprintf("Actividad nueva en %s", title)
	}
}

// mentionBody renders the body for a per-thread mention push. A single aviso
// shows its full phrasing; several in the same thread collapse to a count.
func mentionBody(m MentionActivity) string {
	if m.Count <= 1 {
		if m.Text != "" {
			return m.Text
		}
		if m.Author != "" && m.Target != "" {
			return fmt.Sprintf("%s te ha citado en %s", m.Author, m.Target)
		}
	}
	if m.Target != "" {
		return fmt.Sprintf("%d nuevos avisos en %s", m.Count, m.Target)
	}
	return fmt.Sprintf("%d nuevos avisos", m.Count)
}
