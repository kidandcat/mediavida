package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	stdtls "crypto/tls"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	tls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// ForumMessage represents a single post from the forum thread.
type ForumMessage struct {
	Author   string
	Body     string // plain text (backward compatible)
	BodyHTML string // rich inner HTML of .post-contents, URLs absolutized
	Avatar   string // absolute avatar URL
	Date      string // human date, e.g. "2/1/25 a las 16:37"
	Time     int64  // unix seconds, 0 if unknown
	Num      int
	Liked    bool // true if the current user has liked this post
}

// ErrGuardRequired is returned when login requires email PIN verification.
type ErrGuardRequired struct {
	GuardURL string
}

func (e *ErrGuardRequired) Error() string {
	return "guard verification required: check your email for the PIN"
}

// ForumScraper handles login and thread scraping from Mediavida.
type ForumScraper struct {
	client       *http.Client
	user         string
	pass         string
	loggedIn     bool
	csrfToken    string // CSRF token from page, needed for post_mola and replies
	threadID     string // numeric thread ID from page hidden input
	forumID      string // forum ID from page hidden input #fid
	pageNum      string // current page number from hidden input #pagina
	threadURL    string // full thread URL for referer
	clientID     string // for per-client session persistence
	lastSaveTime time.Time // tracks last session save to avoid excessive writes
	totpSecret   string // base32 TOTP secret for automatic guard verification
}

// utlsDialTLS dials a TLS connection using utls with a Chrome fingerprint.
func utlsDialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		conn.Close()
		return nil, err
	}
	tlsConn := tls.UClient(conn, &tls.Config{ServerName: host}, tls.HelloChrome_Auto)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

// newChromeTransport creates an http2.Transport that uses utls with a Chrome
// TLS fingerprint, bypassing Cloudflare bot detection while supporting HTTP/2.
func newChromeTransport() http.RoundTripper {
	return &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *stdtls.Config) (net.Conn, error) {
			return utlsDialTLS(ctx, network, addr)
		},
	}
}

func NewForumScraper(user, pass, clientID string) *ForumScraper {
	jar, _ := cookiejar.New(nil)
	return &ForumScraper{
		client: &http.Client{
			Jar:       jar,
			Transport: newChromeTransport(),
			Timeout:   30 * time.Second,
		},
		user:     user,
		pass:     pass,
		clientID: clientID,
	}
}

func (s *ForumScraper) doGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	// Don't set Accept-Encoding manually — http2.Transport handles gzip automatically
	req.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	return s.client.Do(req)
}

func (s *ForumScraper) Login() error {
	resp, err := s.doGet("https://www.mediavida.com/login")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return err
	}

	token, ok := doc.Find("#_token").Attr("value")
	if !ok {
		return fmt.Errorf("no CSRF token found")
	}

	// POST login — use a client that doesn't follow redirects so we can detect the guard
	noRedirClient := *s.client
	noRedirClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	loginData := url.Values{
		"name":     {s.user},
		"password": {s.pass},
		"cookie":   {"1"},
		"_token":   {token},
	}
	loginReq, err := http.NewRequest("POST", "https://www.mediavida.com/login", strings.NewReader(loginData.Encode()))
	if err != nil {
		return err
	}
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	loginReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	loginReq.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	loginReq.Header.Set("Origin", "https://www.mediavida.com")
	loginReq.Header.Set("Referer", "https://www.mediavida.com/login")
	loginReq.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	loginReq.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	loginReq.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	loginReq.Header.Set("Sec-Fetch-Dest", "document")
	loginReq.Header.Set("Sec-Fetch-Mode", "navigate")
	loginReq.Header.Set("Sec-Fetch-Site", "same-origin")
	loginReq.Header.Set("Sec-Fetch-User", "?1")
	loginReq.Header.Set("Upgrade-Insecure-Requests", "1")
	loginResp, err := noRedirClient.Do(loginReq)
	if err != nil {
		return err
	}
	// Log full response for debugging
	body, _ := io.ReadAll(loginResp.Body)
	loginResp.Body.Close()
	log.Printf("Login POST: status=%d, location=%q", loginResp.StatusCode, loginResp.Header.Get("Location"))
	if loginResp.StatusCode != 302 {
		// Parse error from HTML if present
		if strings.Contains(string(body), "class=\"err\"") || strings.Contains(string(body), "error") {
			log.Printf("Login page body (first 500 chars): %s", string(body[:min(500, len(body))]))
		}
	}

	// Check for guard redirect (email/TOTP verification)
	if loginResp.StatusCode == 302 {
		loc := loginResp.Header.Get("Location")
		if strings.Contains(loc, "/login/guard/") {
			guardURL := loc
			if !strings.HasPrefix(guardURL, "http") {
				guardURL = "https://www.mediavida.com" + guardURL
			}
			return &ErrGuardRequired{GuardURL: guardURL}
		} else {
			// Normal redirect (success) — follow it
			_, err = s.doGet("https://www.mediavida.com" + loc)
			if err != nil {
				return err
			}
		}
	}

	// Verify login
	resp, err = s.doGet("https://www.mediavida.com/")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	u, _ := url.Parse("https://www.mediavida.com")
	cookies := s.client.Jar.Cookies(u)
	hasSession := false
	for _, c := range cookies {
		if c.Name != "_l" {
			hasSession = true
			break
		}
	}
	if !hasSession {
		return fmt.Errorf("login failed: no session cookie received")
	}

	s.loggedIn = true
	log.Println("Logged in to Mediavida")
	return nil
}

// SubmitGuard posts the email verification code and verifies the resulting session.
func (s *ForumScraper) SubmitGuard(guardURL, code string) error {
	data := url.Values{
		"code":            {code},
		"remember_device": {"1"},
		"device":          {""},
	}

	req, err := http.NewRequest("POST", guardURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", guardURL)
	req.Header.Set("Origin", "https://www.mediavida.com")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	log.Printf("Guard response: status=%d, url=%s", resp.StatusCode, resp.Request.URL)

	// Verify login by checking session cookies
	resp, err = s.doGet("https://www.mediavida.com/")
	if err != nil {
		return err
	}
	resp.Body.Close()

	u, _ := url.Parse("https://www.mediavida.com")
	cookies := s.client.Jar.Cookies(u)
	hasSession := false
	for _, c := range cookies {
		if c.Name != "_l" {
			hasSession = true
			break
		}
	}
	if !hasSession {
		return fmt.Errorf("verification failed: invalid code")
	}

	s.loggedIn = true
	log.Println("Guard verification successful, logged in")
	return nil
}

// sessionFile returns the path to the session cookie file.
// If clientID is non-empty, returns a per-client session file.
func sessionFile(clientID string) string {
	dir, _ := os.UserConfigDir()
	if clientID == "" {
		return filepath.Join(dir, "mediavida-mcp", "session.json")
	}
	return filepath.Join(dir, "mediavida-mcp", "session-"+clientID+".json")
}

// savedSession is the JSON-serializable session state including credentials and cookies.
type savedSession struct {
	User    string        `json:"user"`
	Pass    string        `json:"pass"`
	Cookies []savedCookie `json:"cookies"`
}

// savedCookie is the JSON-serializable representation of an HTTP cookie.
type savedCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Expires  time.Time `json:"expires,omitempty"`
	MaxAge   int       `json:"max_age,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HttpOnly bool      `json:"http_only,omitempty"`
}

// SaveSession persists cookies and credentials to disk so the user doesn't need to log in every time.
func (s *ForumScraper) SaveSession() {
	u, _ := url.Parse("https://www.mediavida.com")
	cookies := s.client.Jar.Cookies(u)
	var saved []savedCookie
	for _, c := range cookies {
		expires := c.Expires
		maxAge := c.MaxAge
		// If no expiry set, assign a long-lived expiry (30 days) to match browser behavior.
		// Browsers keep "remember me" cookies for weeks; Go's cookiejar loses this info.
		if expires.IsZero() && maxAge == 0 {
			expires = time.Now().Add(30 * 24 * time.Hour)
		}
		saved = append(saved, savedCookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   "www.mediavida.com",
			Path:     "/",
			Expires:  expires,
			MaxAge:   maxAge,
			Secure:   c.Secure,
			HttpOnly: c.HttpOnly,
		})
	}
	sess := savedSession{User: s.user, Pass: s.pass, Cookies: saved}
	path := sessionFile(s.clientID)
	os.MkdirAll(filepath.Dir(path), 0700)
	data, _ := json.Marshal(sess)
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("Failed to save session: %v", err)
	}
	s.lastSaveTime = time.Now()
}

// AutoSave saves the session if at least 2 minutes have passed since the last save.
// Call this after tool operations to keep cookies fresh without excessive disk writes.
func (s *ForumScraper) AutoSave() {
	if time.Since(s.lastSaveTime) > 2*time.Minute {
		s.SaveSession()
	}
}

// LoadSession restores cookies and credentials from disk. Returns true if a session was loaded.
func (s *ForumScraper) LoadSession() bool {
	data, err := os.ReadFile(sessionFile(s.clientID))
	if err != nil {
		return false
	}

	// Try new format (with credentials)
	var sess savedSession
	if err := json.Unmarshal(data, &sess); err == nil && len(sess.Cookies) > 0 {
		if sess.User != "" {
			s.user = sess.User
			s.pass = sess.Pass
		}
		u, _ := url.Parse("https://www.mediavida.com")
		var cookies []*http.Cookie
		for _, sc := range sess.Cookies {
			// Skip expired cookies
			if !sc.Expires.IsZero() && sc.Expires.Before(time.Now()) {
				log.Printf("Skipping expired cookie %s (expired %s)", sc.Name, sc.Expires.Format(time.RFC3339))
				continue
			}
			cookies = append(cookies, &http.Cookie{
				Name:     sc.Name,
				Value:    sc.Value,
				Domain:   sc.Domain,
				Path:     sc.Path,
				Expires:  sc.Expires,
				MaxAge:   sc.MaxAge,
				Secure:   sc.Secure,
				HttpOnly: sc.HttpOnly,
			})
		}
		if len(cookies) == 0 {
			log.Printf("All cookies expired for user %s", s.user)
			return false
		}
		s.client.Jar.(*cookiejar.Jar).SetCookies(u, cookies)
		s.loggedIn = true
		log.Printf("Session restored from disk (user: %s, %d cookies)", s.user, len(cookies))
		return true
	}

	// Fallback: old format (array of cookies only)
	var saved []savedCookie
	if err := json.Unmarshal(data, &saved); err != nil {
		return false
	}
	u, _ := url.Parse("https://www.mediavida.com")
	var cookies []*http.Cookie
	for _, sc := range saved {
		cookies = append(cookies, &http.Cookie{
			Name:     sc.Name,
			Value:    sc.Value,
			Domain:   sc.Domain,
			Path:     sc.Path,
			Expires:  sc.Expires,
			MaxAge:   sc.MaxAge,
			Secure:   sc.Secure,
			HttpOnly: sc.HttpOnly,
		})
	}
	s.client.Jar.(*cookiejar.Jar).SetCookies(u, cookies)
	s.loggedIn = true
	log.Println("Session restored from disk (legacy format)")
	return true
}

// ClearSession removes the saved session file.
func (s *ForumScraper) ClearSession() {
	os.Remove(sessionFile(s.clientID))
	s.loggedIn = false
	// Reset cookie jar so stale cookies don't interfere with re-login
	jar, _ := cookiejar.New(nil)
	s.client.Jar = jar
}

// ValidateSession checks if the current session is actually valid by making
// a lightweight request to the bubbles endpoint.
func (s *ForumScraper) ValidateSession() bool {
	if !s.loggedIn {
		return false
	}
	req, err := http.NewRequest("GET", "https://www.mediavida.com/usuarios/action/bubbles.php", nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(body))
	// HTML response or empty means session is invalid
	if len(trimmed) == 0 || trimmed[0] == '<' {
		return false
	}
	// Empty JSON array also means not logged in
	var arr []int
	if json.Unmarshal(body, &arr) == nil && len(arr) == 0 {
		return false
	}
	return true
}

// LikeMessage sends a "mano" (like) to a post via POST /foro/post_mola.php.
func (s *ForumScraper) LikeMessage(postNum int) error {
	if !s.loggedIn {
		if err := s.Login(); err != nil {
			return fmt.Errorf("login failed: %w", err)
		}
	}

	if s.threadID == "" {
		return fmt.Errorf("no thread ID set")
	}
	if s.csrfToken == "" {
		return fmt.Errorf("no CSRF token available")
	}

	data := url.Values{
		"token": {s.csrfToken},
		"tid":   {s.threadID},
		"num":   {strconv.Itoa(postNum)},
		"undo":  {"false"},
	}

	req, err := http.NewRequest("POST", "https://www.mediavida.com/foro/post_mola.php", strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	req.Header.Set("Referer", s.threadURL)
	req.Header.Set("Origin", "https://www.mediavida.com")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("mola request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mola returned status %d: %s", resp.StatusCode, string(body))
	}
	log.Printf("Mano sent to post #%d (tid=%s, response: %s)", postNum, s.threadID, string(body))
	return nil
}

// GetPostSource returns the raw (editable) source text of a post in the
// last-read thread. Fetch the thread first so tid/token are set.
func (s *ForumScraper) GetPostSource(postNum int) (string, error) {
	if !s.loggedIn {
		return "", fmt.Errorf("not logged in")
	}
	if s.threadID == "" || s.csrfToken == "" {
		return "", fmt.Errorf("no thread loaded; read the thread first")
	}
	data := url.Values{
		"tid":   {s.threadID},
		"num":   {strconv.Itoa(postNum)},
		"token": {s.csrfToken},
	}
	req, err := http.NewRequest("POST", "https://www.mediavida.com/foro/post_contents.php", strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", s.threadURL)
	req.Header.Set("Origin", "https://www.mediavida.com")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("post source request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("post source returned %d", resp.StatusCode)
	}
	return string(body), nil
}

// GetQuotedPost fetches a single post of the last-read thread by number,
// as the web does when expanding a #NNNN reference (post_quote.php).
// Returns the author and the rendered, URL-absolutized HTML body.
func (s *ForumScraper) GetQuotedPost(postNum int) (author, bodyHTML string, err error) {
	if !s.loggedIn {
		return "", "", fmt.Errorf("not logged in")
	}
	if s.threadID == "" || s.csrfToken == "" {
		return "", "", fmt.Errorf("no thread loaded; read the thread first")
	}
	data := url.Values{
		"num":   {strconv.Itoa(postNum)},
		"tid":   {s.threadID},
		"token": {s.csrfToken},
	}
	req, e := http.NewRequest("POST", "https://www.mediavida.com/foro/post_quote.php", strings.NewReader(data.Encode()))
	if e != nil {
		return "", "", e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", s.threadURL)
	req.Header.Set("Origin", "https://www.mediavida.com")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, e := s.client.Do(req)
	if e != nil {
		return "", "", fmt.Errorf("quote request failed: %w", e)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("quote returned %d", resp.StatusCode)
	}
	// post_quote.php returns JSON {status, min, full} with the quote source text.
	var res struct {
		Status int    `json:"status"`
		Min    string `json:"min"`
		Full   string `json:"full"`
	}
	if e := json.Unmarshal(body, &res); e != nil {
		return "", strings.TrimSpace(string(body)), nil
	}
	text := res.Full
	if text == "" {
		text = res.Min
	}
	return "", text, nil
}

// EditMessage edits an existing post in the last-read thread (poster.php with a
// num parameter edits instead of creating a new reply). Fetch the thread first.
func (s *ForumScraper) EditMessage(postNum int, text string) error {
	if !s.loggedIn {
		return fmt.Errorf("not logged in")
	}
	if s.threadID == "" || s.csrfToken == "" {
		return fmt.Errorf("no thread loaded; read the thread first")
	}
	data := url.Values{
		"cuerpo": {text},
		"tid":    {s.threadID},
		"num":    {strconv.Itoa(postNum)},
		"token":  {s.csrfToken},
	}
	req, err := http.NewRequest("POST", "https://www.mediavida.com/foro/action/poster.php", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Referer", s.threadURL)
	req.Header.Set("Origin", "https://www.mediavida.com")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("edit request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("edit returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body[:min(200, len(body))])))
	}
	// Response is JSON {"status":1,"msg":...} on success.
	var res struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(body, &res); err == nil && res.Status != 1 {
		return fmt.Errorf("edit rejected by mediavida: %s", strings.TrimSpace(string(body[:min(200, len(body))])))
	}
	return nil
}


// PostReply posts a reply to the current thread. If replyToNum > 0, prepends #NUM to reference that post.
func (s *ForumScraper) PostReply(text string, replyToNum int) error {
	if !s.loggedIn {
		return fmt.Errorf("not logged in")
	}
	if s.csrfToken == "" || s.threadID == "" || s.forumID == "" {
		return fmt.Errorf("missing thread metadata (token/tid/fid)")
	}

	body := text
	if replyToNum > 0 {
		body = fmt.Sprintf("#%d %s", replyToNum, text)
	}

	data := url.Values{
		"token":    {s.csrfToken},
		"cabecera": {""},
		"fid":      {s.forumID},
		"tid":      {s.threadID},
		"pagina":   {s.pageNum},
		"cuerpo":   {body},
	}

	req, err := http.NewRequest("POST", "https://www.mediavida.com/foro/action/poster.php", strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	req.Header.Set("Origin", "https://www.mediavida.com")
	req.Header.Set("Referer", s.threadURL)
	req.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("reply request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("Reply POST: status=%d (replyTo=#%d, len=%d)", resp.StatusCode, replyToNum, len(body))

	// A successful post redirects (302) to the thread page
	if resp.StatusCode >= 400 {
		return fmt.Errorf("reply failed: status %d: %s", resp.StatusCode, string(respBody[:min(200, len(respBody))]))
	}

	return nil
}

// SearchResult represents a single search result from the forum.
type SearchResult struct {
	Title  string
	URL    string
	Forum  string
	Author string
	Date   string
}

// Search performs a forum search via DuckDuckGo (Mediavida uses client-side Google CSE which can't be scraped).
func (s *ForumScraper) Search(query string) ([]SearchResult, error) {
	searchURL := "https://html.duckduckgo.com/html/?q=site%3Amediavida.com%2Fforo+" + url.QueryEscape(query)
	resp, err := s.doGet(searchURL)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse search results: %w", err)
	}

	var results []SearchResult
	doc.Find(".result").Each(func(i int, sel *goquery.Selection) {
		// Skip ads
		if sel.HasClass("result--ad") {
			return
		}
		a := sel.Find("a.result__a").First()
		title := strings.TrimSpace(a.Text())
		rawHref, _ := a.Attr("href")
		href := extractDDGURL(rawHref)
		snippet := strings.TrimSpace(sel.Find(".result__snippet").Text())

		if title != "" && strings.Contains(href, "mediavida.com") {
			results = append(results, SearchResult{
				Title: title,
				URL:   href,
				Date:  snippet,
			})
		}
	})

	return results, nil
}

// extractDDGURL extracts the actual URL from a DuckDuckGo redirect link.
func extractDDGURL(rawURL string) string {
	if strings.Contains(rawURL, "uddg=") {
		u, err := url.Parse(rawURL)
		if err == nil {
			if uddg := u.Query().Get("uddg"); uddg != "" {
				return uddg
			}
		}
		// Try with scheme prefix
		if strings.HasPrefix(rawURL, "//") {
			u, err = url.Parse("https:" + rawURL)
			if err == nil {
				if uddg := u.Query().Get("uddg"); uddg != "" {
					return uddg
				}
			}
		}
	}
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	return rawURL
}

// ThreadListItem represents a thread from favorites or user posts listings.
type ThreadListItem struct {
	Title        string
	URL          string
	Forum        string
	Replies      string
	LastActivity string
	UnreadCount  string // number of unread posts, empty if none
	UnreadURL    string // link to the first unread post (.../<page>#<num>), if any
}

// parseThreadList parses threads from the table structure used by favorites and posts pages.
func parseThreadList(doc *goquery.Document) []ThreadListItem {
	var items []ThreadListItem
	doc.Find("#temas tr").Each(func(i int, sel *goquery.Selection) {
		// Bold links (a.hb) mark threads with unread posts; a.h are read.
		a := sel.Find("td.col-th .thread a.h, td.col-th .thread a.hb").First()
		title := strings.TrimSpace(a.AttrOr("title", a.Text()))
		href := a.AttrOr("href", "")
		if href != "" && !strings.HasPrefix(href, "http") {
			href = "https://www.mediavida.com" + href
		}
		forum := sel.Find("td.autor-avatar a.tooltip").AttrOr("title", "")
		replies := strings.TrimSpace(sel.Find("td.thread-count .num.reply").Text())
		activity := sel.Find("td.last-av .m_date").First().AttrOr("title", "")
		if activity == "" {
			activity = sel.Find("td.thread-count .rd").First().AttrOr("title", "")
		}
		// Unread count: the badge is span.unread-g > a.unseen-num ("3").
		unread := strings.TrimSpace(sel.Find(".unseen-num").Text())
		if unread == "" {
			unread = strings.TrimSpace(sel.Find(".sp.sp2").Text())
		}
		if unread == "" {
			unread = strings.TrimSpace(sel.Find("a.sp").Text())
		}
		// Bold but no explicit count → mark as unread anyway.
		if unread == "" && a.HasClass("hb") {
			unread = "new"
		}
		// Link to the first unread post (page + #num anchor).
		unreadURL := sel.Find(".unseen-num").AttrOr("href", "")
		if unreadURL == "" {
			unreadURL = sel.Find("a.sp").AttrOr("href", "")
		}
		if unreadURL != "" && !strings.HasPrefix(unreadURL, "http") {
			unreadURL = "https://www.mediavida.com" + unreadURL
		}
		if title != "" {
			items = append(items, ThreadListItem{
				Title:        title,
				URL:          href,
				Forum:        forum,
				Replies:      replies,
				LastActivity: activity,
				UnreadCount:  unread,
				UnreadURL:    unreadURL,
			})
		}
	})
	return items
}

// FetchFavorites returns the user's favorite threads.
func (s *ForumScraper) FetchFavorites() ([]ThreadListItem, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}
	resp, err := s.doGet("https://www.mediavida.com/foro/favoritos")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	// Favorites uses ul.content.threads > li, not #temas table
	var items []ThreadListItem
	doc.Find("ul.content.threads li").Each(func(i int, sel *goquery.Selection) {
		// Thread link is the second <a> inside div (first is forum icon)
		threadLink := sel.Find("div a.h, div a.hb").First()
		title := threadLink.AttrOr("title", strings.TrimSpace(threadLink.Text()))
		href := threadLink.AttrOr("href", "")
		if href != "" && !strings.HasPrefix(href, "http") {
			href = "https://www.mediavida.com" + href
		}

		// Try multiple selectors for unread count badge
		unread := strings.TrimSpace(sel.Find(".unseen-num").Text())
		if unread == "" {
			unread = strings.TrimSpace(sel.Find(".sp.sp2").Text())
		}
		if unread == "" {
			unread = strings.TrimSpace(sel.Find(".count-new").Text())
		}
		if unread == "" {
			unread = strings.TrimSpace(sel.Find("a.sp").Text())
		}
		// Check if the thread link class indicates unread (hb = has bold = has new)
		if unread == "" && threadLink.HasClass("hb") {
			// Thread has new posts but we couldn't find a specific count
			unread = "new"
		}
		unreadURL := sel.Find(".unseen-num").AttrOr("href", "")
		if unreadURL == "" {
			unreadURL = sel.Find("a.sp").AttrOr("href", "")
		}
		if unreadURL != "" && !strings.HasPrefix(unreadURL, "http") {
			unreadURL = "https://www.mediavida.com" + unreadURL
		}

		if title != "" {
			items = append(items, ThreadListItem{
				Title:       title,
				URL:         href,
				UnreadCount: unread,
				UnreadURL:   unreadURL,
			})
		}
	})

	// Log raw HTML structure for debugging when items are found but no unread counts
	if len(items) > 0 {
		hasUnread := false
		for _, item := range items {
			if item.UnreadCount != "" {
				hasUnread = true
				break
			}
		}
		if !hasUnread {
			// Log first list item's HTML for debugging
			first := doc.Find("ul.content.threads li").First()
			html, _ := first.Html()
			if len(html) > 500 {
				html = html[:500]
			}
			log.Printf("[favorites] no unread counts found. First li HTML: %s", html)
		}
	}

	// Fallback to table parser if ul structure not found
	if len(items) == 0 {
		items = parseThreadList(doc)
		if len(items) == 0 {
			// Log page structure for debugging
			bodyHTML, _ := doc.Find("body").Html()
			if len(bodyHTML) > 1000 {
				bodyHTML = bodyHTML[:1000]
			}
			log.Printf("[favorites] no items found with any parser. Body preview: %s", bodyHTML)
		}
	}
	return items, nil
}

// FetchUserPosts returns threads where the given user has posted.
func (s *ForumScraper) FetchUserPosts(username string) ([]ThreadListItem, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}
	resp, err := s.doGet(fmt.Sprintf("https://www.mediavida.com/id/%s/posts", username))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseThreadList(doc), nil
}

// MentionItem represents a mention of the user in a thread.
type MentionItem struct {
	ThreadTitle string
	ThreadURL   string
	Author      string
	PostNum     int
	Body        string
	Date        string
}

// FetchMentions returns recent mentions of the given user.
func (s *ForumScraper) FetchMentions(username string) ([]MentionItem, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}
	resp, err := s.doGet(fmt.Sprintf("https://www.mediavida.com/id/%s/menciones", username))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	var items []MentionItem
	doc.Find("div.post").Each(func(i int, sel *goquery.Selection) {
		snum := sel.AttrOr("data-num", "0")
		num, _ := strconv.Atoi(snum)
		author := sel.AttrOr("data-autor", "")
		threadLink := sel.Find(".post-meta h1 a").First()
		threadTitle := strings.TrimSpace(threadLink.Text())
		threadHref := threadLink.AttrOr("href", "")
		if threadHref != "" && !strings.HasPrefix(threadHref, "http") {
			threadHref = "https://www.mediavida.com" + threadHref
		}
		body := strings.TrimSpace(sel.Find(".post-contents").Text())
		date := sel.Find(".post-meta .rd").First().AttrOr("title", "")
		if threadTitle != "" {
			items = append(items, MentionItem{
				ThreadTitle: threadTitle,
				ThreadURL:   threadHref,
				Author:      author,
				PostNum:     num,
				Body:        body,
				Date:        date,
			})
		}
	})
	return items, nil
}

// NotificationItem represents one entry of the user's notifications page
// (citas, group posts, etc.) — the feed behind the "bn" bubble counter.
type NotificationItem struct {
	ID     string
	Author string
	Avatar string
	Text   string // full phrasing, e.g. "MisKo te ha citado en <thread>"
	Target string // the linked thread/section title
	URL    string // absolute link to the post/section
	Time   string
	Unseen bool // set by the caller from the unread counter
}

// FetchNotifications returns the user's notifications. Visiting this page marks
// all notifications as seen server-side (Mediavida has no per-item mark), so the
// bn bubble drops to 0 after this call.
func (s *ForumScraper) FetchNotifications() ([]NotificationItem, error) {
	if !s.loggedIn {
		return nil, &ErrSessionExpired{}
	}
	resp, err := s.doGet("https://www.mediavida.com/notificaciones")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	var items []NotificationItem
	doc.Find("ul.notify > li[id]").Each(func(i int, li *goquery.Selection) {
		body := li.Find(".firma-body").First()
		bq := body.Find("blockquote").First()
		if bq.Length() == 0 {
			return
		}
		action := bq.Find("a.action").First()
		items = append(items, NotificationItem{
			ID:     strings.TrimPrefix(li.AttrOr("id", ""), "f_"),
			Author: strings.TrimSpace(bq.Find("a.autor").First().Text()),
			Avatar: absolutizeMV(li.Find(".firma-avatar img").First().AttrOr("src", "")),
			Text:   strings.Join(strings.Fields(bq.Text()), " "),
			Target: strings.TrimSpace(action.Text()),
			URL:    absolutizeMV(action.AttrOr("href", "")),
			Time:   strings.TrimSpace(body.Find("span.n").First().Text()),
		})
	})
	return items, nil
}

// Bubbles represents the notification counters from MV's bubbles.php endpoint.
type Bubbles struct {
	Messages      int `json:"bm"`
	Notifications int `json:"bn"`
	Favorites     int `json:"bf"`
}

// ErrSessionExpired is returned when the MV session cookies are no longer valid.
type ErrSessionExpired struct{}

func (e *ErrSessionExpired) Error() string {
	return "mediavida session expired"
}

// FetchBubbles polls the lightweight notification endpoint to check for new activity.
func (s *ForumScraper) FetchBubbles() (*Bubbles, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}

	req, err := http.NewRequest("GET", "https://www.mediavida.com/usuarios/action/bubbles.php", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	req.Header.Set("Referer", "https://www.mediavida.com/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bubbles request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bubbles returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("bubbles read failed: %w", err)
	}

	log.Printf("[bubbles] raw response (%d bytes): %s", len(body), string(body[:min(200, len(body))]))

	// Non-JSON response (HTML) means Cloudflare blocked us or session expired
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 || trimmed[0] == '<' {
		s.loggedIn = false
		return nil, &ErrSessionExpired{}
	}

	// bubbles.php may return an object {"bm":0,"bn":0,"bf":0} or an array [0,0,0]
	var b Bubbles
	if err := json.Unmarshal(body, &b); err != nil {
		// Try array format [bm, bn, bf]
		var arr []int
		if err2 := json.Unmarshal(body, &arr); err2 != nil {
			return nil, fmt.Errorf("bubbles decode failed: %w (body: %s)", err, string(body[:min(200, len(body))]))
		}
		// Empty array [] also means not logged in
		if len(arr) == 0 {
			s.loggedIn = false
			return nil, &ErrSessionExpired{}
		}
		if len(arr) >= 3 {
			b = Bubbles{Messages: arr[0], Notifications: arr[1], Favorites: arr[2]}
		}
	}
	return &b, nil
}

// ModBubbles represents moderation counters for a single subforum.
type ModBubbles struct {
	ForumSlug string
	ForumID   string
	Reports   int
	Messages  int // mod messages (not PMs)
}

// FetchModBubbles fetches the subforum index page and extracts the mod dropdown
// counters (Reportes (N), Mensajes (N)). Returns nil with no error if the user
// is not a mod of the given subforum (no dropdown present).
func (s *ForumScraper) FetchModBubbles(forumSlug string) (*ModBubbles, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}

	forumURL := fmt.Sprintf("https://www.mediavida.com/foro/%s", forumSlug)
	resp, err := s.doGet(forumURL)
	if err != nil {
		return nil, fmt.Errorf("mod bubbles request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mod bubbles returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("mod bubbles parse failed: %w", err)
	}

	mb := &ModBubbles{ForumSlug: forumSlug}
	var found bool
	doc.Find("ul.dropdown-menu a").EachWithBreak(func(i int, sel *goquery.Selection) bool {
		href, _ := sel.Attr("href")
		text := strings.TrimSpace(sel.Text())
		if strings.Contains(href, "/foro/reportes.php") {
			if fid := extractFid(href); fid != "" {
				mb.ForumID = fid
			}
			mb.Reports = extractParenCount(text)
			found = true
		} else if strings.Contains(href, "/mensajes/mod") {
			if mb.ForumID == "" {
				if fid := extractFid(href); fid != "" {
					mb.ForumID = fid
				}
			}
			mb.Messages = extractParenCount(text)
			found = true
		}
		return true
	})

	if !found {
		// User is not a mod of this subforum (no dropdown present)
		return nil, nil
	}
	return mb, nil
}

// ModReport represents a single pending report in /foro/reportes.php.
type ModReport struct {
	ThreadID    string // e.g. "730678"
	PostNum     string // e.g. "3852"
	ThreadTitle string // full title incl. "#N" suffix
	PostURL     string // absolute URL to the reported post
	PostAuthor  string
	PostBody    string // plain text, trimmed
	Reason      string // reason given by the reporter
	Reporter    string // username who reported
	ReportedAgo string // human-readable elapsed time, e.g. "hace 8 segundos"
}

// Key returns a stable identifier for this report (thread+post), used to dedup.
func (r *ModReport) Key() string {
	return r.ThreadID + ":" + r.PostNum
}

// FetchModReports fetches /foro/reportes.php?fid=X and returns the list of
// pending reports with full detail.
func (s *ForumScraper) FetchModReports(fid string) ([]ModReport, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}
	if fid == "" {
		return nil, fmt.Errorf("empty fid")
	}

	reportsURL := fmt.Sprintf("https://www.mediavida.com/foro/reportes.php?fid=%s", fid)
	resp, err := s.doGet(reportsURL)
	if err != nil {
		return nil, fmt.Errorf("reports request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reports returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reports parse failed: %w", err)
	}

	var reports []ModReport
	doc.Find(".mod-panel .entry").Each(func(i int, sel *goquery.Selection) {
		var r ModReport

		r.ThreadTitle = strings.TrimSpace(sel.Find(".c-main h4 a:not(.foroicon)").First().Text())
		if r.ThreadTitle == "" {
			// Fallback: whole h4 minus icon
			r.ThreadTitle = strings.TrimSpace(sel.Find(".c-main h4").Text())
		}

		verLink := sel.Find(".side-options a[href*='/foro/']").First()
		if href, ok := verLink.Attr("href"); ok {
			if !strings.HasPrefix(href, "http") {
				href = "https://www.mediavida.com" + href
			}
			r.PostURL = href
		}

		r.PostAuthor = strings.TrimSpace(sel.Find(".side-options .autor").Text())

		body := strings.TrimSpace(sel.Find(".msg").Text())
		r.PostBody = collapseWhitespace(body)

		r.Reason = strings.TrimSpace(sel.Find(".c-side .logs li h5").First().Text())

		lightText := strings.TrimSpace(sel.Find(".c-side .logs li .light").First().Text())
		r.Reporter = strings.TrimSpace(sel.Find(".c-side .logs li .light a").First().Text())
		// "reportado por X hace 8 segundos" → extract "hace 8 segundos"
		if idx := strings.LastIndex(lightText, "hace "); idx >= 0 {
			r.ReportedAgo = strings.TrimSpace(lightText[idx:])
		}

		form := sel.Find("form.repaction")
		r.ThreadID = strings.TrimSpace(form.Find("input[name='tid']").AttrOr("value", ""))
		r.PostNum = strings.TrimSpace(form.Find("input[name='num']").AttrOr("value", ""))

		if r.ThreadID == "" || r.PostNum == "" {
			// Skip entries without a usable key
			return
		}
		reports = append(reports, r)
	})

	return reports, nil
}

// collapseWhitespace normalises runs of whitespace (incl. newlines) into a single space.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

// extractParenCount parses "Reportes (3)" → 3. Returns 0 if no match.
func extractParenCount(s string) int {
	open := strings.LastIndex(s, "(")
	closeIdx := strings.LastIndex(s, ")")
	if open < 0 || closeIdx < 0 || closeIdx <= open {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(s[open+1 : closeIdx]))
	if err != nil {
		return 0
	}
	return n
}

// extractFid parses "/foro/reportes.php?fid=169" → "169". Returns empty if no fid.
func extractFid(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return u.Query().Get("fid")
}

// RefreshSession visits the main page to keep the session alive, mimicking browser navigation.
// This causes the server to send fresh Set-Cookie headers, extending session lifetime.
func (s *ForumScraper) RefreshSession() error {
	if !s.loggedIn {
		return fmt.Errorf("not logged in")
	}
	resp, err := s.doGet("https://www.mediavida.com/")
	if err != nil {
		return fmt.Errorf("session refresh failed: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("session refresh returned status %d", resp.StatusCode)
	}
	log.Println("[session] refreshed via main page visit")
	return nil
}

// generateTOTP generates a 6-digit TOTP code from a base32 secret.
func generateTOTP(secret string) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("invalid base32 secret: %w", err)
	}
	counter := uint64(time.Now().Unix() / 30)
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	hash := mac.Sum(nil)
	offset := hash[len(hash)-1] & 0x0f
	code := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7fffffff
	otp := code % uint32(math.Pow10(6))
	return fmt.Sprintf("%06d", otp), nil
}

// SetTOTPSecret sets the TOTP secret for automatic guard verification.
func (s *ForumScraper) SetTOTPSecret(secret string) {
	s.totpSecret = secret
}

// Relogin clears the session and performs a fresh login.
// If guard verification is required and a TOTP secret is configured,
// it automatically generates and submits the code.
func (s *ForumScraper) Relogin() error {
	s.ClearSession()
	if err := s.Login(); err != nil {
		if guardErr, ok := err.(*ErrGuardRequired); ok && s.totpSecret != "" {
			code, err := generateTOTP(s.totpSecret)
			if err != nil {
				return fmt.Errorf("TOTP generation failed: %w", err)
			}
			log.Printf("Guard required, submitting TOTP code automatically")
			if err := s.SubmitGuard(guardErr.GuardURL, code); err != nil {
				return fmt.Errorf("auto guard verification failed: %w", err)
			}
			s.SaveSession()
			return nil
		}
		return err
	}
	s.SaveSession()
	return nil
}

// IsLoggedIn returns whether the scraper believes it has an active session.
func (s *ForumScraper) IsLoggedIn() bool {
	return s.loggedIn
}

// InboxItem represents a conversation in the private messages inbox.
type InboxItem struct {
	ID     string
	Author string
	Date   string
	Short  string
	Unread bool
}

// FetchInbox returns the list of private message conversations.
func (s *ForumScraper) FetchInbox(page int) ([]InboxItem, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}
	if page <= 0 {
		page = 1
	}

	req, err := http.NewRequest("GET", fmt.Sprintf("https://www.mediavida.com/mensajes/fly/%d", page), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8")
	req.Header.Set("Referer", "https://www.mediavida.com/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inbox request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inbox returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var items []InboxItem
	doc.Find("li").Each(func(i int, sel *goquery.Selection) {
		a := sel.Find("a").First()
		href := a.AttrOr("href", "")
		id := strings.TrimPrefix(href, "/mensajes/")

		author := strings.TrimSpace(sel.Find("strong").Text())
		date := strings.TrimSpace(sel.Find("abbr").Text())
		short := strings.TrimSpace(sel.Find("span.short").Text())
		unread := sel.HasClass("unread")

		if id != "" && author != "" {
			items = append(items, InboxItem{
				ID:     id,
				Author: author,
				Date:   date,
				Short:  short,
				Unread: unread,
			})
		}
	})

	return items, nil
}

// PrivateMessage represents a single message in a conversation.
type PrivateMessage struct {
	Author string
	Date   string
	Body   string
}

// Conversation holds the messages and metadata of a PM conversation.
type Conversation struct {
	Title    string
	Messages []PrivateMessage
}

// FetchConversation reads a private message conversation by its ID.
func (s *ForumScraper) FetchConversation(id string) (*Conversation, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}

	resp, err := s.doGet(fmt.Sprintf("https://www.mediavida.com/mensajes/%s", id))
	if err != nil {
		return nil, fmt.Errorf("conversation request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("conversation returned status %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	title := strings.TrimSpace(doc.Find("h2").First().Text())

	var msgs []PrivateMessage
	// Private-message conversations render each message as
	//   li.pm > .wrap > .pm-info (.autor, .rd) + .post-contents.post-msg
	// Consecutive messages from the same author render as li.pm.nested WITHOUT
	// their own .pm-info, so carry the last known author forward.
	lastAuthor := ""
	doc.Find(".post-contents").Each(func(i int, sel *goquery.Selection) {
		body := strings.TrimSpace(sel.Text())
		if body == "" {
			return
		}
		meta := sel.Parent().Find(".pm-info")
		if meta.Length() == 0 {
			meta = sel.Closest(".wrap").Find(".pm-info")
		}
		if meta.Length() == 0 {
			meta = sel.Parent().Find(".post-meta")
		}
		author := strings.TrimSpace(meta.Find(".autor").First().Text())
		if author == "" {
			author = lastAuthor
		} else {
			lastAuthor = author
		}
		rd := meta.Find(".rd").First()
		date := rd.AttrOr("title", strings.TrimSpace(rd.Text()))
		msgs = append(msgs, PrivateMessage{Author: author, Date: date, Body: body})
	})

	// Fallback to the classic forum-post structure if nothing matched.
	if len(msgs) == 0 {
		doc.Find("div.post, div.cf.post").Each(func(i int, sel *goquery.Selection) {
			author := sel.AttrOr("data-autor", "")
			if author == "" {
				author = strings.TrimSpace(sel.Find(".post-meta .autor a").Text())
			}
			date := strings.TrimSpace(sel.Find(".post-meta .rd").AttrOr("title", ""))
			body := strings.TrimSpace(sel.Find(".post-contents").Text())
			if body != "" {
				msgs = append(msgs, PrivateMessage{Author: author, Date: date, Body: body})
			}
		})
	}

	return &Conversation{Title: title, Messages: msgs}, nil
}

// SendPrivateMessage posts a reply into an existing private-message conversation.
// It re-reads the conversation page to obtain a fresh CSRF token + thread id,
// then submits the message form (POST /msg/pm_action.php).
func (s *ForumScraper) SendPrivateMessage(conversationID, text string) error {
	if !s.loggedIn {
		return fmt.Errorf("not logged in")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("empty message")
	}

	convURL := fmt.Sprintf("https://www.mediavida.com/mensajes/%s", conversationID)
	resp, err := s.doGet(convURL)
	if err != nil {
		return fmt.Errorf("load conversation failed: %w", err)
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return err
	}

	token := doc.Find("#pm-form #token, #pm-form input[name=token]").AttrOr("value", "")
	if token == "" {
		token = doc.Find("#token").AttrOr("value", "")
	}
	mtid := doc.Find("#pm-form #mtid, #mtid").AttrOr("value", "")
	if mtid == "" {
		mtid = conversationID
	}
	if token == "" {
		return fmt.Errorf("could not find PM CSRF token")
	}

	form := url.Values{
		"token": {token},
		"mtid":  {mtid},
		"msg":   {text},
	}
	req, err := http.NewRequest("POST", "https://www.mediavida.com/msg/pm_action.php", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", "https://www.mediavida.com")
	req.Header.Set("Referer", convURL)
	postResp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send message failed: %w", err)
	}
	defer postResp.Body.Close()
	body, _ := io.ReadAll(postResp.Body)
	if postResp.StatusCode >= 400 {
		return fmt.Errorf("send message returned %d: %s", postResp.StatusCode, strings.TrimSpace(string(body[:min(200, len(body))])))
	}
	return nil
}

// Username returns the logged-in username.
func (s *ForumScraper) Username() string {
	return s.user
}

// TagOption represents an available tag for a subforum's new thread form.
type TagOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NewThreadPage holds the metadata from the new thread form page.
type NewThreadPage struct {
	Token string
	FID   string
	Tags  []TagOption
}

// FetchNewThreadPage visits the new thread form for a subforum and extracts the CSRF token, forum ID, and available tags.
func (s *ForumScraper) FetchNewThreadPage(subforum string) (*NewThreadPage, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}

	pageURL := fmt.Sprintf("https://www.mediavida.com/foro/%s/nuevo-hilo", subforum)
	resp, err := s.doGet(pageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch new thread page: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	token := doc.Find("#token").AttrOr("value", "")
	if token == "" {
		token = doc.Find("input[name=token]").AttrOr("value", "")
	}

	fid := doc.Find("#fid").AttrOr("value", "")
	if fid == "" {
		fid = doc.Find("input[name=fid]").AttrOr("value", "")
	}

	var tags []TagOption
	doc.Find("select[name=tag] option").Each(func(i int, sel *goquery.Selection) {
		id := sel.AttrOr("value", "")
		name := strings.TrimSpace(sel.Text())
		if id != "" && id != "0" {
			tags = append(tags, TagOption{ID: id, Name: name})
		}
	})

	log.Printf("[new-thread] subforum=%s, fid=%s, token=%s...%s, tags=%d",
		subforum, fid, token[:min(6, len(token))], token[max(0, len(token)-6):], len(tags))

	return &NewThreadPage{Token: token, FID: fid, Tags: tags}, nil
}

// CreateThread creates a new thread in the specified subforum via acciones_preview.php.
func (s *ForumScraper) CreateThread(subforum, title, body string, tag int, addToFav bool) (string, error) {
	if !s.loggedIn {
		return "", fmt.Errorf("not logged in")
	}

	page, err := s.FetchNewThreadPage(subforum)
	if err != nil {
		return "", err
	}
	if page.Token == "" {
		return "", fmt.Errorf("no CSRF token found on new thread page")
	}
	if page.FID == "" {
		return "", fmt.Errorf("no forum ID found on new thread page")
	}

	tofav := "0"
	if addToFav {
		tofav = "1"
	}

	data := url.Values{
		"cabecera": {title},
		"tag":      {strconv.Itoa(tag)},
		"cuerpo":   {body},
		"tofav":    {tofav},
		"token":    {page.Token},
		"fid":      {page.FID},
	}

	req, err := http.NewRequest("POST", "https://www.mediavida.com/foro/acciones_preview.php", strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	req.Header.Set("Origin", "https://www.mediavida.com")
	req.Header.Set("Referer", fmt.Sprintf("https://www.mediavida.com/foro/%s/nuevo-hilo", subforum))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-GPC", "1")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("create thread request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("CreateThread POST: status=%d, body=%s", resp.StatusCode, string(respBody[:min(500, len(respBody))]))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("create thread failed: status %d: %s", resp.StatusCode, string(respBody[:min(200, len(respBody))]))
	}

	return string(respBody), nil
}

// ThreadPage holds messages from a single page along with pagination metadata.
type ThreadPage struct {
	Messages    []ForumMessage
	CurrentPage int
	TotalPages  int
}

// FetchThread scrapes a single page of a thread and returns the messages with pagination info.
// If page <= 0, it fetches the last page.
func (s *ForumScraper) FetchThread(threadURL string, page int) (*ThreadPage, error) {
	if !s.loggedIn {
		if err := s.Login(); err != nil {
			return nil, fmt.Errorf("login failed: %w", err)
		}
	}

	s.threadURL = threadURL

	// First fetch page 1 to discover total pages
	resp, err := s.doGet(threadURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	s.extractMetadata(doc)

	totalPages := 1
	doc.Find(".side-pages li a").Each(func(i int, sel *goquery.Selection) {
		if n, err := strconv.Atoi(sel.Text()); err == nil && n > totalPages {
			totalPages = n
		}
	})

	// Default to last page
	targetPage := page
	if targetPage <= 0 {
		targetPage = totalPages
	}
	if targetPage > totalPages {
		targetPage = totalPages
	}

	// If page 1 is the target, parse the already-fetched doc
	if targetPage == 1 {
		messages := s.parseMessages(doc)
		return &ThreadPage{Messages: messages, CurrentPage: 1, TotalPages: totalPages}, nil
	}

	// Otherwise fetch the target page
	pageURL := fmt.Sprintf("%s/%d", threadURL, targetPage)
	messages, err := s.fetchPage(pageURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch page %d: %w", targetPage, err)
	}

	return &ThreadPage{Messages: messages, CurrentPage: targetPage, TotalPages: totalPages}, nil
}

// extractMetadata pulls thread ID, CSRF token, forum ID, and page number from the document.
func (s *ForumScraper) extractMetadata(doc *goquery.Document) {
	if tid := doc.Find("#tid").AttrOr("value", ""); tid != "" {
		s.threadID = tid
		log.Printf("Thread ID: %s", tid)
	}
	if token := doc.Find("#token").AttrOr("value", ""); token != "" {
		s.csrfToken = token
		log.Printf("CSRF token: %s...%s", token[:min(6, len(token))], token[max(0, len(token)-6):])
	}
	if fid := doc.Find("#fid").AttrOr("value", ""); fid != "" {
		s.forumID = fid
	}
	if pagina := doc.Find("#pagina").AttrOr("value", ""); pagina != "" {
		s.pageNum = pagina
	}
}

// parseMessages extracts ForumMessage entries from a goquery document.
func (s *ForumScraper) parseMessages(doc *goquery.Document) []ForumMessage {
	var messages []ForumMessage
	doc.Find(".cf.post").Each(func(i int, sel *goquery.Selection) {
		author, _ := sel.Attr("data-autor")
		snum, _ := sel.Attr("data-num")
		num, _ := strconv.Atoi(snum)

		contents := sel.Find(".post-contents").First()
		body := strings.TrimSpace(contents.Text())

		// De-lazyload images and absolutize all URLs so the HTML renders standalone.
		contents.Find("img[data-src]").Each(func(_ int, img *goquery.Selection) {
			if ds := img.AttrOr("data-src", ""); ds != "" {
				img.SetAttr("src", ds)
			}
		})
		contents.Find("img[src]").Each(func(_ int, img *goquery.Selection) {
			img.SetAttr("src", absolutizeMV(img.AttrOr("src", "")))
		})
		contents.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
			a.SetAttr("href", absolutizeMV(a.AttrOr("href", "")))
		})
		bodyHTML, _ := contents.Html()
		bodyHTML = strings.TrimSpace(bodyHTML)

		avatar := sel.Find(".post-avatar img").First()
		avatarURL := absolutizeMV(avatar.AttrOr("data-src", avatar.AttrOr("src", "")))

		rd := sel.Find(".post-meta .rd").First()
		date := rd.AttrOr("title", strings.TrimSpace(rd.Text()))
		var ts int64
		if dt := rd.AttrOr("data-time", ""); dt != "" {
			if n, err := strconv.ParseInt(dt, 10, 64); err == nil {
				ts = n
			}
		}

		liked := sel.Find("a.post-btn.masmola").AttrOr("data-undo", "false") == "true"
		if body != "" || bodyHTML != "" {
			messages = append(messages, ForumMessage{
				Author: author, Body: body, BodyHTML: bodyHTML,
				Avatar: avatarURL, Date: date, Time: ts, Num: num, Liked: liked,
			})
		}
	})
	return messages
}

func (s *ForumScraper) fetchPage(pageURL string) ([]ForumMessage, error) {
	resp, err := s.doGet(pageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("page not found")
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	s.extractMetadata(doc)
	return s.parseMessages(doc), nil
}
