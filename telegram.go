package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TelegramBot polls Telegram's getUpdates, registers chat IDs when mentioned
// by authorized admins, and sends mod alerts to the registered chats.
type TelegramBot struct {
	token    string
	botName  string // resolved via getMe, e.g. "mv_mod_bot"
	admins   map[int64]string // telegram_user_id → mv_username

	mu    sync.RWMutex
	chats map[string]map[int64]struct{} // mv_username → set of chat_id

	client *http.Client
	stopCh chan struct{}
}

// NewTelegramBot constructs a bot from env vars. Returns nil if TELEGRAM_BOT_TOKEN is unset.
// TELEGRAM_ADMINS format: "12345:jairo,67890:otro" (telegram_user_id:mv_username).
func NewTelegramBot() *TelegramBot {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return nil
	}

	admins := parseTelegramAdmins(os.Getenv("TELEGRAM_ADMINS"))
	if len(admins) == 0 {
		log.Println("[telegram] WARNING: TELEGRAM_BOT_TOKEN set but TELEGRAM_ADMINS is empty — no chats will be registrable")
	}

	tb := &TelegramBot{
		token:  token,
		admins: admins,
		chats:  make(map[string]map[int64]struct{}),
		client: &http.Client{Timeout: 40 * time.Second},
		stopCh: make(chan struct{}),
	}
	tb.loadChats()
	return tb
}

func parseTelegramAdmins(raw string) map[int64]string {
	out := make(map[int64]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		id, user, ok := strings.Cut(pair, ":")
		if !ok {
			continue
		}
		tgID, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
		if err != nil {
			continue
		}
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}
		out[tgID] = user
	}
	return out
}

func telegramChatsFile() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "mediavida-mcp", "telegram_chats.json")
}

func (tb *TelegramBot) loadChats() {
	data, err := os.ReadFile(telegramChatsFile())
	if err != nil {
		return
	}
	var m map[string][]int64
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for user, ids := range m {
		set := make(map[int64]struct{}, len(ids))
		for _, id := range ids {
			set[id] = struct{}{}
		}
		tb.chats[user] = set
	}
	log.Printf("[telegram] restored chats for %d user(s) from disk", len(m))
}

func (tb *TelegramBot) saveChats() {
	tb.mu.RLock()
	snapshot := make(map[string][]int64, len(tb.chats))
	for user, set := range tb.chats {
		ids := make([]int64, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		snapshot[user] = ids
	}
	tb.mu.RUnlock()

	data, _ := json.Marshal(snapshot)
	path := telegramChatsFile()
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Printf("[telegram] failed to save chats: %v", err)
	}
}

// Start launches the getUpdates long-poll loop in a goroutine.
func (tb *TelegramBot) Start() {
	if tb == nil {
		return
	}

	name, err := tb.fetchBotUsername()
	if err != nil {
		log.Printf("[telegram] getMe failed: %v (bot disabled)", err)
		return
	}
	tb.botName = name
	log.Printf("[telegram] bot @%s ready, %d admin(s) configured", tb.botName, len(tb.admins))

	go tb.pollUpdates()
}

// Stop halts the polling goroutine.
func (tb *TelegramBot) Stop() {
	if tb == nil {
		return
	}
	close(tb.stopCh)
}

type telegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	Title string `json:"title"`
}

type telegramMessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

type telegramMessage struct {
	MessageID int                     `json:"message_id"`
	From      *telegramUser           `json:"from"`
	Chat      telegramChat            `json:"chat"`
	Text      string                  `json:"text"`
	Entities  []telegramMessageEntity `json:"entities"`
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

func (tb *TelegramBot) apiURL(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", tb.token, method)
}

func (tb *TelegramBot) fetchBotUsername() (string, error) {
	resp, err := tb.client.Get(tb.apiURL("getMe"))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tr telegramResponse[telegramUser]
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("decode: %w (body: %s)", err, string(body))
	}
	if !tr.OK {
		return "", fmt.Errorf("getMe: %s", tr.Description)
	}
	return tr.Result.Username, nil
}

func (tb *TelegramBot) pollUpdates() {
	var offset int64
	for {
		select {
		case <-tb.stopCh:
			log.Println("[telegram] polling stopped")
			return
		default:
		}

		url := tb.apiURL("getUpdates") + fmt.Sprintf("?timeout=30&offset=%d&allowed_updates=%%5B%%22message%%22%%5D", offset)
		resp, err := tb.client.Get(url)
		if err != nil {
			log.Printf("[telegram] getUpdates failed: %v (retrying in 5s)", err)
			time.Sleep(5 * time.Second)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var tr telegramResponse[[]telegramUpdate]
		if err := json.Unmarshal(body, &tr); err != nil {
			log.Printf("[telegram] decode updates failed: %v (body: %s)", err, string(body))
			time.Sleep(5 * time.Second)
			continue
		}
		if !tr.OK {
			log.Printf("[telegram] getUpdates error: %s", tr.Description)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, upd := range tr.Result {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
			if upd.Message != nil {
				log.Printf("[telegram] update: chat=%d (%s) from=%d (@%s) text=%q entities=%d",
					upd.Message.Chat.ID, upd.Message.Chat.Type,
					fromID(upd.Message.From), fromUsername(upd.Message.From),
					upd.Message.Text, len(upd.Message.Entities))
				tb.handleMessage(upd.Message)
			}
		}
	}
}

func fromID(u *telegramUser) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

func fromUsername(u *telegramUser) string {
	if u == nil {
		return ""
	}
	return u.Username
}

func (tb *TelegramBot) handleMessage(msg *telegramMessage) {
	if msg.From == nil {
		return
	}
	if !tb.mentionsBot(msg) {
		return
	}

	mvUser, isAdmin := tb.admins[msg.From.ID]
	if !isAdmin {
		log.Printf("[telegram] ignoring mention from non-admin user %d (@%s) in chat %d",
			msg.From.ID, msg.From.Username, msg.Chat.ID)
		tb.sendMessage(msg.Chat.ID, "No tienes permiso para usar este bot.")
		return
	}

	tb.mu.Lock()
	set, ok := tb.chats[mvUser]
	if !ok {
		set = make(map[int64]struct{})
		tb.chats[mvUser] = set
	}
	_, alreadyRegistered := set[msg.Chat.ID]
	set[msg.Chat.ID] = struct{}{}
	tb.mu.Unlock()

	go tb.saveChats()

	var reply string
	if alreadyRegistered {
		reply = fmt.Sprintf("Este chat ya estaba registrado para notificaciones de mod de %s.", mvUser)
	} else {
		reply = fmt.Sprintf("Listo. Enviaré aquí las notificaciones de mod de %s.", mvUser)
	}
	tb.sendMessage(msg.Chat.ID, reply)
	log.Printf("[telegram] registered chat %d (%s) for %s", msg.Chat.ID, msg.Chat.Title, mvUser)
}

func (tb *TelegramBot) mentionsBot(msg *telegramMessage) bool {
	if tb.botName == "" {
		return false
	}
	needle := "@" + strings.ToLower(tb.botName)
	for _, e := range msg.Entities {
		if e.Type != "mention" {
			continue
		}
		if e.Offset < 0 || e.Offset+e.Length > len(msg.Text) {
			continue
		}
		mention := strings.ToLower(msg.Text[e.Offset : e.Offset+e.Length])
		if mention == needle {
			return true
		}
	}
	return false
}

// SendModAlert sends a message to all registered chats for the given mv_username.
// Safe to call even if the bot is nil or the user has no chats.
func (tb *TelegramBot) SendModAlert(mvUsername, text string) {
	if tb == nil {
		return
	}
	tb.mu.RLock()
	set := tb.chats[mvUsername]
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	tb.mu.RUnlock()

	if len(ids) == 0 {
		return
	}
	for _, chatID := range ids {
		tb.sendMessage(chatID, text)
	}
}

func (tb *TelegramBot) sendMessage(chatID int64, text string) {
	payload := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
		"parse_mode":               "HTML",
	}
	data, _ := json.Marshal(payload)
	resp, err := tb.client.Post(tb.apiURL("sendMessage"), "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("[telegram] sendMessage to %d failed: %v", chatID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[telegram] sendMessage to %d status %d: %s", chatID, resp.StatusCode, string(body))
	}
}
