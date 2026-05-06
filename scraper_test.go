package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// loadEnv reads key=value pairs from .env file into environment.
func loadEnv(t *testing.T) {
	t.Helper()
	f, err := os.Open(".env")
	if err != nil {
		t.Skip("no .env file found — skipping integration test")
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			os.Setenv(k, v)
		}
	}
}

// TestLoginFlow tests the login flow against the real Mediavida server.
// Requires .env with MV_USERNAME and MV_PASSWORD.
// Run with: go test -run TestLoginFlow -v
func TestLoginFlow(t *testing.T) {
	loadEnv(t)
	user := os.Getenv("MV_USERNAME")
	pass := os.Getenv("MV_PASSWORD")
	if user == "" || pass == "" {
		t.Skip("MV_USERNAME or MV_PASSWORD not set — skipping integration test")
	}

	s := NewForumScraper(user, pass, "")

	// Step 1: GET /login — should return the login page with CSRF token
	t.Log("Step 1: GET /login")
	resp, err := s.doGet("https://www.mediavida.com/login")
	if err != nil {
		t.Fatalf("GET /login failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	t.Logf("  Status: %d", resp.StatusCode)
	t.Logf("  Body length: %d", len(body))

	bodyStr := string(body)
	if strings.Contains(bodyStr, "Just a moment") {
		t.Fatal("Cloudflare challenge detected — TLS fingerprint bypass not working")
	}
	if !strings.Contains(bodyStr, "_token") {
		t.Fatal("CSRF token not found in login page")
	}
	t.Log("  CSRF token found")

	// Step 2: Full Login()
	t.Log("Step 2: Full Login()")
	s2 := NewForumScraper(user, pass, "")
	err = s2.Login()
	if err != nil {
		if fmt.Sprintf("%T", err) == "*main.ErrGuardRequired" {
			t.Logf("  Guard required (login succeeded, needs email PIN): %v", err)
		} else {
			t.Fatalf("  Login failed: %v", err)
		}
	} else {
		t.Log("  Login succeeded without guard")
	}
}

func TestSearchDebug(t *testing.T) {
	loadEnv(t)
	user := os.Getenv("MV_USERNAME")
	pass := os.Getenv("MV_PASSWORD")
	if user == "" || pass == "" {
		t.Skip("MV_USERNAME or MV_PASSWORD not set")
	}

	s := NewForumScraper(user, pass, "")
	if !s.LoadSession() {
		t.Skip("no saved session")
	}

	// Try different search URL formats
	urls := []string{
		"https://www.mediavida.com/buscar.php?q=IA",
		"https://www.mediavida.com/foro/buscar?q=agentes+ia",
	}

	noRedir := *s.client
	noRedir.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	for _, u := range urls {
		req, _ := http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		resp, err := noRedir.Do(req)
		if err != nil {
			t.Logf("[%s] error: %v", u, err)
			continue
		}
		loc := resp.Header.Get("Location")
		t.Logf("[%s] status=%d location=%q", u, resp.StatusCode, loc)
		resp.Body.Close()
	}

	// Fetch search results and show HTML structure
	resp, err := s.doGet("https://www.mediavida.com/buscar.php?q=agentes+ia")
	if err != nil {
		t.Fatalf("GET buscar.php failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	t.Logf("Search results: status=%d url=%s bodyLen=%d", resp.StatusCode, resp.Request.URL, len(body))

	html := string(body)
	// Show the last 5000 chars of the page (the content/results area)
	if len(html) > 5000 {
		t.Logf("Last 5000 chars:\n%s", html[len(html)-5000:])
	} else {
		t.Logf("Full body:\n%s", html)
	}

	// Test favorites page
	t.Log("\n--- Favorites ---")
	resp2, err := s.doGet("https://www.mediavida.com/foro/favoritos")
	if err != nil {
		t.Fatalf("Favorites error: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	html2 := string(body2)
	// Show from content div onwards
	idx := strings.Index(html2, "<div id=\"content\">")
	if idx < 0 {
		idx = strings.Index(html2, "id=\"content\"")
	}
	if idx >= 0 {
		end := min(len(html2), idx+4000)
		t.Logf("Favorites content:\n%s", html2[idx:end])
	}

	// Test mentions page
	t.Log("\n--- Mentions ---")
	resp3, err := s.doGet("https://www.mediavida.com/id/kidandcat/menciones")
	if err != nil {
		t.Fatalf("Mentions error: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	html3 := string(body3)
	idx = strings.Index(html3, "<div id=\"content\">")
	if idx < 0 {
		idx = strings.Index(html3, "id=\"content\"")
	}
	if idx >= 0 {
		end := min(len(html3), idx+4000)
		t.Logf("Mentions content:\n%s", html3[idx:end])
	}

	// Test DuckDuckGo HTML search as alternative
	t.Log("\n--- DuckDuckGo search ---")
	ddgURL := "https://html.duckduckgo.com/html/?q=site%3Amediavida.com%2Fforo+agentes+ia"
	resp5, err := s.doGet(ddgURL)
	if err != nil {
		t.Logf("DDG error: %v", err)
	} else {
		body5, _ := io.ReadAll(resp5.Body)
		resp5.Body.Close()
		out5 := string(body5)
		// Find the results area
		if idx := strings.Index(out5, "result__"); idx >= 0 {
			end := min(len(out5), idx+2000)
			t.Logf("DDG results (status=%d bodyLen=%d):\n%s", resp5.StatusCode, len(body5), out5[max(0, idx-100):end])
		} else {
			if len(out5) > 2000 {
				out5 = out5[:2000]
			}
			t.Logf("DDG status=%d bodyLen=%d body:\n%s", resp5.StatusCode, len(body5), out5)
		}
	}

	// Test participated threads (user's posts)
	t.Log("\n--- User posts ---")
	resp4, err := s.doGet("https://www.mediavida.com/id/kidandcat/posts")
	if err != nil {
		t.Logf("User posts error: %v", err)
	} else {
		body4, _ := io.ReadAll(resp4.Body)
		resp4.Body.Close()
		html4 := string(body4)
		idx = strings.Index(html4, "<div id=\"content\">")
		if idx < 0 {
			idx = strings.Index(html4, "id=\"content\"")
		}
		if idx >= 0 {
			end := min(len(html4), idx+4000)
			t.Logf("User posts content:\n%s", html4[idx:end])
		} else {
			t.Logf("User posts: no content div (bodyLen=%d, url=%s)", len(html4), resp4.Request.URL)
		}
	}
}
