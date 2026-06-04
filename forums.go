package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// absolutizeMV turns a relative MV URL into an absolute one. Absolute,
// protocol-relative and data: URLs are returned unchanged.
func absolutizeMV(u string) string {
	u = strings.TrimSpace(u)
	if u == "" || strings.HasPrefix(u, "http") || strings.HasPrefix(u, "//") || strings.HasPrefix(u, "data:") {
		return u
	}
	if strings.HasPrefix(u, "/") {
		return "https://www.mediavida.com" + u
	}
	return u
}

// ForumInfo is a single subforum entry in the forum index.
type ForumInfo struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Icon        string `json:"icon,omitempty"` // e.g. "fid-9"
}

// ForumCategory groups subforums under a heading (e.g. "Tecnología").
type ForumCategory struct {
	Name   string      `json:"name"`
	Forums []ForumInfo `json:"forums"`
}

// ForumThread is a thread row in a subforum listing.
type ForumThread struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	URL          string `json:"url"`
	Author       string `json:"author"`
	Tag          string `json:"tag,omitempty"`
	Replies      string `json:"replies"`
	Views        string `json:"views"`
	LastActivity string `json:"last_activity"`
	LastTime     int64  `json:"last_time,omitempty"` // unix seconds, 0 if unknown
	Pinned       bool   `json:"pinned"`
	Closed       bool   `json:"closed"`
	UnreadCount  string `json:"unread_count,omitempty"`
}

// ForumThreadList is one page of a subforum's thread listing.
type ForumThreadList struct {
	Subforum    string        `json:"subforum"`
	CurrentPage int           `json:"current_page"`
	TotalPages  int           `json:"total_pages"`
	Threads     []ForumThread `json:"threads"`
}

// FetchForumIndex returns the forum index grouped by category.
func (s *ForumScraper) FetchForumIndex() ([]ForumCategory, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}
	resp, err := s.doGet("https://www.mediavida.com/foro")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var cats []ForumCategory
	doc.Find("ul.forums").Each(func(_ int, ul *goquery.Selection) {
		cat := ForumCategory{Name: strings.TrimSpace(ul.PrevFiltered("h2").Text())}
		ul.Find("li a.forum").Each(func(_ int, a *goquery.Selection) {
			href := a.AttrOr("href", "")
			slug := strings.TrimPrefix(href, "/foro/")
			slug = strings.Trim(slug, "/")
			if slug == "" || strings.Contains(slug, "/") {
				return
			}
			name := strings.TrimSpace(a.Find(".info-col strong").First().Text())
			desc := strings.TrimSpace(a.Find(".info-col p.ddkb").First().Text())
			icon := ""
			if cls, ok := a.Find(".icon-col i.fid").Attr("class"); ok {
				for _, c := range strings.Fields(cls) {
					if strings.HasPrefix(c, "fid-") {
						icon = c
						break
					}
				}
			}
			if name == "" {
				name = slug
			}
			cat.Forums = append(cat.Forums, ForumInfo{Slug: slug, Name: name, Description: desc, Icon: icon})
		})
		if len(cat.Forums) > 0 {
			cats = append(cats, cat)
		}
	})
	return cats, nil
}

// FetchForumThreads returns one page of a subforum's thread listing.
// page <= 1 fetches the first page.
func (s *ForumScraper) FetchForumThreads(slug string, page int) (*ForumThreadList, error) {
	if !s.loggedIn {
		return nil, fmt.Errorf("not logged in")
	}
	url := "https://www.mediavida.com/foro/" + slug
	if page > 1 {
		url = fmt.Sprintf("%s/p%d", url, page)
	}
	resp, err := s.doGet(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	out := &ForumThreadList{Subforum: slug, CurrentPage: page}
	if out.CurrentPage < 1 {
		out.CurrentPage = 1
	}

	doc.Find("#temas tr").Each(func(_ int, tr *goquery.Selection) {
		a := tr.Find("td.col-th .thread a.h, td.col-th .thread a.hb").First()
		title := strings.TrimSpace(a.AttrOr("title", a.Text()))
		if title == "" {
			return
		}
		href := a.AttrOr("href", "")
		if href != "" && !strings.HasPrefix(href, "http") {
			href = "https://www.mediavida.com" + href
		}
		id := strings.TrimPrefix(a.AttrOr("id", ""), "a")

		// Author: prefer the avatar img alt (clean username), fall back to /id/ link.
		author := strings.TrimSpace(tr.Find("td.col-av img").AttrOr("alt", ""))
		if author == "" {
			author = strings.TrimSpace(tr.Find("td.col-av a[href^='/id/']").First().Text())
		}

		tag := strings.TrimSpace(tr.Find("td.col-th .tag-group").Text())
		replies := strings.TrimSpace(tr.Find("td.thread-count .num.reply").First().Text())
		views := strings.TrimSpace(tr.Find("td.dtc .num").First().Text())

		rd := tr.Find("td.thread-count .rd").First()
		lastActivity := rd.AttrOr("title", strings.TrimSpace(rd.Text()))
		var lastTime int64
		if dt := rd.AttrOr("data-time", ""); dt != "" {
			if n, perr := strconv.ParseInt(dt, 10, 64); perr == nil {
				lastTime = n
			}
		}

		pinned := tr.Find(".tema-status .fa-thumb-tack").Length() > 0
		closed := tr.HasClass("locked") && !pinned

		unread := strings.TrimSpace(tr.Find(".unseen-num").Text())
		if unread == "" {
			unread = strings.TrimSpace(tr.Find(".sp.sp2").Text())
		}
		if unread == "" {
			unread = strings.TrimSpace(tr.Find("a.sp").Text())
		}

		out.Threads = append(out.Threads, ForumThread{
			ID:           id,
			Title:        title,
			URL:          href,
			Author:       author,
			Tag:          tag,
			Replies:      replies,
			Views:        views,
			LastActivity: lastActivity,
			LastTime:     lastTime,
			Pinned:       pinned,
			Closed:       closed,
			UnreadCount:  unread,
		})
	})

	// Total pages = highest /foro/<slug>/p<N> link found in the pager.
	prefix := "/foro/" + slug + "/p"
	out.TotalPages = out.CurrentPage
	doc.Find("a[href]").Each(func(_ int, a *goquery.Selection) {
		href := a.AttrOr("href", "")
		href = strings.TrimPrefix(href, "https://www.mediavida.com")
		if !strings.HasPrefix(href, prefix) {
			return
		}
		seg := strings.TrimPrefix(href, prefix)
		seg = strings.SplitN(seg, "/", 2)[0]
		seg = strings.SplitN(seg, "?", 2)[0]
		seg = strings.SplitN(seg, "#", 2)[0]
		if n, perr := strconv.Atoi(seg); perr == nil && n > out.TotalPages {
			out.TotalPages = n
		}
	})

	return out, nil
}
