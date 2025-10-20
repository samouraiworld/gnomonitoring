package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type chat struct {
	ID int64 `json:"id"`
}
type message struct {
	Chat chat `json:"chat"`
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

func GetChatIDs(botToken string) (ids []int64, nextOffset int, err error) {
	u := "https://api.telegram.org/bot" + url.PathEscape(botToken) + "/getUpdates"

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return nil, 0, fmt.Errorf("telegram HTTP %d", resp.StatusCode)
	}

	var p updatesResp
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, 0, err
	}
	if !p.Ok {
		return nil, 0, fmt.Errorf("ok=false")
	}

	seen := make(map[int64]struct{})
	last := -1
	for _, up := range p.Result {
		if up.UpdateID > last {
			last = up.UpdateID
		}
		for _, m := range []*message{up.Message, up.ChannelPost} {
			if m == nil || m.Chat.ID == 0 {
				continue
			}
			if _, ok := seen[m.Chat.ID]; !ok {
				seen[m.Chat.ID] = struct{}{}
				ids = append(ids, m.Chat.ID)
			}
		}
	}
	if last >= 0 {
		nextOffset = last + 1
	}
	return ids, nextOffset, nil
}

func SendMessageTelegram(botToken string, chatID int64, text string) error {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", botToken)

	body := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
		// tu peux aussi activer le Markdown ou HTML si besoin :
		// "parse_mode": "MarkdownV2", // ou "HTML"
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBody))
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

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telegram HTTP %d", resp.StatusCode)
	}

	var res struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if !res.Ok {
		return fmt.Errorf("telegram error: %s", res.Description)
	}

	return nil
}
func MsgTelegram(Msg string, Token string) (err error) {

	if Token == "" {
		return fmt.Errorf("token is empty")
	}

	ids, next, err := GetChatIDs(Token)
	if err != nil {
		panic(err)
	}
	fmt.Println("chat_ids:", ids)
	fmt.Println("nextOffset:", next)

	for _, chatID := range ids {
		fmt.Println("Send to chat:", chatID)

		err := SendMessageTelegram(Token, chatID, "MSG")
		if err != nil {
			fmt.Printf("❌ Error for  %d: %v\n", chatID, err)
		} else {
			fmt.Printf("✅ Msg Send à %d\n", chatID)
		}
	}

	return nil
}
