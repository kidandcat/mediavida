package main

import "testing"

func TestThreadKey(t *testing.T) {
	cases := map[string]string{
		"https://www.mediavida.com/foro/feedback/hilo-de-prueba-123456":       "t123456",
		"https://www.mediavida.com/foro/feedback/hilo-de-prueba-123456/5":     "t123456",
		"https://www.mediavida.com/foro/feedback/hilo-de-prueba-123456/5#789": "t123456",
		"https://www.mediavida.com/foro/off-topic/algo-1#post":                "t1",
		"https://www.mediavida.com/foro/feedback/sin-id":                      "sin-id",
		"":                                                                    "",
	}
	for in, want := range cases {
		if got := threadKey(in); got != want {
			t.Errorf("threadKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUnread(t *testing.T) {
	cases := map[string]int{"3": 3, "12": 12, "": 0, "new": 0, "-1": 0, " 4 ": 4}
	for in, want := range cases {
		if got := parseUnread(in); got != want {
			t.Errorf("parseUnread(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFavBody(t *testing.T) {
	if got := favBody(1, "Hilo"); got != "1 mensaje nuevo en Hilo" {
		t.Errorf("favBody(1) = %q", got)
	}
	if got := favBody(3, "Hilo"); got != "3 mensajes nuevos en Hilo" {
		t.Errorf("favBody(3) = %q", got)
	}
	if got := favBody(0, "Hilo"); got != "Actividad nueva en Hilo" {
		t.Errorf("favBody(0) = %q", got)
	}
}

func TestMentionBody(t *testing.T) {
	single := MentionActivity{Text: "MisKo te ha citado en El Hilo", Target: "El Hilo", Count: 1}
	if got := mentionBody(single); got != "MisKo te ha citado en El Hilo" {
		t.Errorf("mentionBody single = %q", got)
	}
	multi := MentionActivity{Target: "El Hilo", Count: 3}
	if got := mentionBody(multi); got != "3 nuevos avisos en El Hilo" {
		t.Errorf("mentionBody multi = %q", got)
	}
}

func TestBuildActivityPushes_Enriched(t *testing.T) {
	prev := &Bubbles{}
	current := &Bubbles{Notifications: 2, Favorites: 1, Messages: 1}
	fav := []ThreadActivity{{Title: "Hilo Fav", URL: "https://x/foro/a/hilo-10", Key: "t10", Count: 3}}
	mentions := []MentionActivity{{Key: "t20", Text: "User te ha citado en Otro", Target: "Otro", URL: "https://x/foro/a/otro-20", Count: 1}}

	pushes := buildActivityPushes(prev, current, fav, mentions)

	var gotMention, gotFav, gotPM bool
	for _, p := range pushes {
		switch p.data["type"] {
		case "mention":
			gotMention = true
			if p.tag != "nt20" {
				t.Errorf("mention tag = %q, want nt20", p.tag)
			}
			if p.body != "User te ha citado en Otro" {
				t.Errorf("mention body = %q", p.body)
			}
		case "favorite":
			gotFav = true
			if p.tag != "ft10" {
				t.Errorf("favorite tag = %q, want ft10", p.tag)
			}
			if p.body != "3 mensajes nuevos en Hilo Fav" {
				t.Errorf("favorite body = %q", p.body)
			}
		case "pm":
			gotPM = true
			if p.tag != "mensajes" {
				t.Errorf("pm tag = %q", p.tag)
			}
		}
	}
	if !gotMention || !gotFav || !gotPM {
		t.Errorf("missing push: mention=%v fav=%v pm=%v", gotMention, gotFav, gotPM)
	}
}

func TestBuildActivityPushes_Fallback(t *testing.T) {
	// Counters rose but enrichment came back empty → count-only fallback.
	prev := &Bubbles{}
	current := &Bubbles{Notifications: 2, Favorites: 1}
	pushes := buildActivityPushes(prev, current, nil, nil)
	if len(pushes) != 2 {
		t.Fatalf("want 2 fallback pushes, got %d", len(pushes))
	}
	for _, p := range pushes {
		if p.data["type"] == "mention" && p.tag != "avisos" {
			t.Errorf("aviso fallback tag = %q", p.tag)
		}
		if p.data["type"] == "favorite" && p.tag != "favoritos" {
			t.Errorf("favoritos fallback tag = %q", p.tag)
		}
	}
}

func TestBuildActivityPushes_NoChange(t *testing.T) {
	b := &Bubbles{Notifications: 5, Favorites: 2, Messages: 1}
	if pushes := buildActivityPushes(b, b, nil, nil); len(pushes) != 0 {
		t.Errorf("no rise should yield no pushes, got %d", len(pushes))
	}
}
