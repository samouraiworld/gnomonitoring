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
type update struct {
	UpdateID    int      `json:"update_id"`
	Message     *message `json:"message,omitempty"`
	ChannelPost *message `json:"channel_post,omitempty"`
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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
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

func MsgTelegram(Msg string, Token, TypeChatid string, db *gorm.DB) (err error) {

	if Token == "" {
		return fmt.Errorf("token is empty")
	}

	ids, err := database.GetAllChatIDs(db, TypeChatid)
	if err != nil {
		panic(err)
	}
	fmt.Println("chat_ids:", ids)

	for _, chatID := range ids {
		fmt.Println("Send to chat:", chatID)

		err := SendMessageTelegram(Token, chatID, Msg)
		if err != nil {
			fmt.Printf("❌ Error for  %d: %v\n", chatID, err)
		} else {
			fmt.Printf("✅ Msg Send  %d\n Msg: %s", chatID, Msg)
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

func StartCommandLoop(stopCtx context.Context, token string, handlers map[string]func(int64, string), TypeChatid string, db *gorm.DB) error {
	base := "https://api.telegram.org/bot" + url.PathEscape(token) + "/getUpdates"
	offset := 0
	httpClient := &http.Client{Timeout: 50 * time.Second}

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
			if up.Message == nil || up.Message.Chat.ID == 0 {
				continue
			}
			// For save Chat ID into db
			insert, err := database.InsertChatID(db, up.Message.Chat.ID, TypeChatid)
			if err != nil {
				return fmt.Errorf("error  insert chatid to db %w", err)
			}
			if insert && TypeChatid == "govdao" {
				//for send ultimate Govdao
				log.Println("Send ultimate govdao telegram")
				govdaolist, err := database.GetLastGovDaoInfo(db)
				if err != nil {
					return fmt.Errorf("error get lastid govdao%s ", err)

				}
				SendReportGovdaoTelegram(govdaolist.Id, govdaolist.Title, govdaolist.Url, govdaolist.Tx, token, up.Message.Chat.ID)
			}
			//========================
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
