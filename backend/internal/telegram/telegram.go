package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"

	"gorm.io/gorm"
)

var telegramHTTPClient = &http.Client{Timeout: 10 * time.Second}

//	type chat struct {
//		ID int64 `json:"id"`
//	}
//
//	type message struct {
//		Chat chat `json:"chat"`
//	}
type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // optionnel mais utile (private/group/...)
}

type message struct {
	MessageID int        `json:"message_id"`
	Chat      chat       `json:"chat"`
	Text      string     `json:"text"`
	Entities  []tgEntity `json:"entities"`
}
type callbackQuery struct {
	ID      string   `json:"id"`
	Message *message `json:"message,omitempty"`
	Data    string   `json:"data"`
}
type update struct {
	UpdateID      int            `json:"update_id"`
	Message       *message       `json:"message,omitempty"`
	ChannelPost   *message       `json:"channel_post,omitempty"`
	CallbackQuery *callbackQuery `json:"callback_query,omitempty"`
}
type updatesResp struct {
	Ok     bool     `json:"ok"`
	Result []update `json:"result"`
}
type tgEntity struct {
	Type   string `json:"type"`   // "bot_command"
	Offset int    `json:"offset"` // position dans msg.Text
	Length int    `json:"length"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

func SendMessageTelegram(botToken string, chatID int64, text string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	body := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")


	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	var res struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	// on décode TOUJOURS (même si 400) pour récupérer Description
	_ = json.NewDecoder(resp.Body).Decode(&res)

	if resp.StatusCode/100 != 2 || !res.Ok {
		return fmt.Errorf("telegram http %d: %s", resp.StatusCode, res.Description)
	}
	return nil
}

func SendMessageTelegramWithMarkup(botToken string, chatID int64, text string, markup *InlineKeyboardMarkup) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	body := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if markup != nil {
		body["reply_markup"] = markup
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")


	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	var res struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if resp.StatusCode/100 != 2 || !res.Ok {
		return fmt.Errorf("telegram http %d: %s", resp.StatusCode, res.Description)
	}
	return nil
}

func EditMessageTelegramWithMarkup(botToken string, chatID int64, messageID int, text string, markup *InlineKeyboardMarkup) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/editMessageText", botToken)

	body := map[string]any{
		"chat_id":                  chatID,
		"message_id":               messageID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if markup != nil {
		body["reply_markup"] = markup
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")


	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	var res struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)
	if resp.StatusCode/100 != 2 || !res.Ok {
		return fmt.Errorf("telegram http %d: %s", resp.StatusCode, res.Description)
	}
	return nil
}

func AnswerCallbackQuery(botToken, callbackID string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", botToken)
	body := map[string]any{
		"callback_query_id": callbackID,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")


	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	return nil
}

func MsgTelegram(msg string, token, typeChatid string, db *gorm.DB) (err error) {

	if token == "" {
		return fmt.Errorf("token is empty")
	}

	ids, err := database.GetAllChatIDs(db, typeChatid)
	if err != nil {
		log.Printf("❌ GetAllChatIDs failed: %v", err)
		return err
	}

	for _, chatID := range ids {

		err := SendMessageTelegram(token, chatID, msg)
		if err != nil {
			fmt.Printf("❌ Error for  %d: %v\n", chatID, err)
		} else {
			fmt.Printf("✅ Msg Send  %d\n Msg: %s", chatID, msg)
		}
	}

	return nil
}
// MsgTelegramAlert sends msg to every chat that has an active subscription for
// the given (chainID, addr) pair. chainID is used to scope the subscription
// lookup so that alerts from one chain do not bleed into chats that are
// monitoring a different chain.
func MsgTelegramAlert(msg string, addr, chainID, token, typeChatid string, db *gorm.DB) (err error) {

	if token == "" {
		return fmt.Errorf("token is empty")
	}

	ids, err := database.GetAllChatIDs(db, typeChatid)
	if err != nil {
		log.Printf("❌ GetAllChatIDs failed: %v", err)
		return err
	}

	for _, chatID := range ids {
		// check sub scoped to the chain that produced the alert
		subs, err := database.GetTelegramValidatorSub(db, chatID, chainID, true)
		if err != nil {
			log.Printf("⚠️  get subscriptions failed for chat_id=%d: %v", chatID, err)
			continue
		}
		// if empty not send
		if len(subs) == 0 {
			log.Printf("↪️  skip chat_id=%d (no active subscriptions)", chatID)
			continue
		}

		for _, s := range subs {

			if s.Addr == addr {

				if err := SendMessageTelegram(token, chatID, msg); err != nil {
					log.Printf("❌ send failed for chat_id=%d: %v", chatID, err)
					continue
				} else {
					log.Printf("✅ message sent to chat_id=%d (validator=%s)", chatID, addr)
				}
				break
			}
		}

	}

	return nil
}

// ============================== handler telegram ==============================
func extractCommand(msg *message) (cmd, args string, ok bool) {
	if msg == nil || msg.Text == "" || len(msg.Entities) == 0 {
		return
	}
	for _, e := range msg.Entities {
		if e.Type == "bot_command" && e.Offset == 0 && e.Length <= len(msg.Text) {
			raw := msg.Text[:e.Length]                    // ex: "/status@MonBot"
			parts := strings.SplitN(raw, "@", 2)          // delete @MonBot
			cmd = strings.ToLower(parts[0])               // "/status"
			args = strings.TrimSpace(msg.Text[e.Length:]) // get arguments
			return cmd, args, true
		}
	}
	return
}

// StartCommandLoop continuously reads getUpdates and calls the handlers.
// - token: ton bot token
// - stopCtx: to shut down properly (SIGINT/SIGTERM)

// StartCommandLoop continuously reads getUpdates and calls the handlers.
// chainID is used when creating the hour-report row for a new validator chat.
// Pass an empty string for bots that do not need chain-scoped hour reports
// (e.g. govdao).
func StartCommandLoop(stopCtx context.Context, token string, handlers map[string]func(int64, string), callbackHandler func(int64, int, string), typeChatid string, db *gorm.DB, chainID ...string) error {
	base := "https://api.telegram.org/bot" + url.PathEscape(token) + "/getUpdates"
	offset := 0
	httpClient := &http.Client{Timeout: 50 * time.Second}

	// Resolve the default chain for InsertChatID.
	defaultChain := ""
	if len(chainID) > 0 {
		defaultChain = chainID[0]
	}

	// Hydrate chatChainState from DB for validator bots on startup.
	if typeChatid == "validator" {
		prefs, err := database.GetAllChatChains(db)
		if err != nil {
			log.Printf("⚠️ StartCommandLoop: failed to hydrate chatChainState: %v", err)
		} else {
			for chatID, cid := range prefs {
				setActiveChain(chatID, cid)
			}
			log.Printf("ℹ️ Hydrated chain preferences for %d validator chats", len(prefs))
		}
	}

	// Hydrate govdaoChatChainState from DB for govdao bots on startup.
	if typeChatid == "govdao" {
		prefs, err := database.GetAllGovdaoChatChains(db)
		if err != nil {
			log.Printf("⚠️ StartCommandLoop: failed to hydrate govdao chatChainState: %v", err)
		} else {
			for chatID, cid := range prefs {
				setGovdaoActiveChain(chatID, cid)
			}
			log.Printf("ℹ️ Hydrated chain preferences for %d govdao chats", len(prefs))
		}
	}

	for {
		select {
		case <-stopCtx.Done():
			return nil
		default:
		}

		url := base + "?timeout=30"
		if offset > 0 {
			url += fmt.Sprintf("&offset=%d", offset)
		}

		resp, err := httpClient.Get(url)
		if err != nil {
			time.Sleep(2 * time.Second) // petit backoff réseau
			continue
		}
		var payload updatesResp
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			time.Sleep(1 * time.Second)
			continue
		}
		resp.Body.Close()
		if !payload.Ok {
			time.Sleep(2 * time.Second)
			continue
		}

		for _, up := range payload.Result {
			if up.UpdateID >= offset {
				offset = up.UpdateID + 1
			}
			if up.CallbackQuery != nil {
				if up.CallbackQuery.Message == nil || up.CallbackQuery.Message.Chat.ID == 0 {
					continue
				}
				_ = AnswerCallbackQuery(token, up.CallbackQuery.ID)
				if callbackHandler != nil {
					go callbackHandler(
						up.CallbackQuery.Message.Chat.ID,
						up.CallbackQuery.Message.MessageID,
						up.CallbackQuery.Data,
					)
				}
				continue
			}
			if up.Message == nil || up.Message.Chat.ID == 0 {
				continue
			}
			// For save Chat ID into db (fire-and-forget, idempotent upsert)
			chatIDToInsert := up.Message.Chat.ID
			go func() {
				insert, err := database.InsertChatID(db, chatIDToInsert, typeChatid, defaultChain)
				if err != nil {
					log.Printf("⚠️ InsertChatID failed for chat_id=%d: %v", chatIDToInsert, err)
					return
				}
				if insert && typeChatid == "govdao" {
					log.Println("Send ultimate govdao telegram")
					govdaolist, err := database.GetLastGovDaoInfo(db)
					if err != nil {
						log.Printf("error get lastid govdao: %s", err)
						return
					}
					if err := SendReportGovdaoTelegram(govdaolist.Id, govdaolist.Title, govdaolist.Url, govdaolist.Tx, token, chatIDToInsert); err != nil {
						log.Printf("❌ SendReportGovdaoTelegram failed for chat_id=%d: %v", chatIDToInsert, err)
					}
				}
			}()
			if HandleSearchInput(token, db, typeChatid, up.Message.Chat.ID, up.Message.Text) {
				continue
			}
			// ========================
			cmd, args, ok := extractCommand(up.Message)
			if !ok {
				continue
			}
			if h, found := handlers[cmd]; found {
				go h(up.Message.Chat.ID, args) // async to not to block
			} else if h, found := handlers["*"]; found {
				go h(up.Message.Chat.ID, cmd+" "+args)
			}
		}
	}
}

func parseParams(args string) map[string]string {
	out := map[string]string{}
	for _, tok := range strings.Fields(args) { // split by spaces

		tok = strings.TrimPrefix(tok, "--")
		kv := strings.SplitN(tok, "=", 2)
		if len(kv) == 2 {
			k := strings.ToLower(strings.TrimSpace(kv[0]))
			v := strings.Trim(kv[1], `"'`)
			out[k] = v
		}
	}
	return out
}
