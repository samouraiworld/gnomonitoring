package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"sort"
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

func GetChatIDs(botToken, TypeChatid string, db *gorm.DB) (nextOffset int, err error) {
	u := "https://api.telegram.org/bot" + url.PathEscape(botToken) + "/getUpdates"

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return 0, fmt.Errorf("telegram HTTP %d", resp.StatusCode)
	}

	var p updatesResp
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return 0, err
	}
	if !p.Ok {
		return 0, fmt.Errorf("ok=false")
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
				insert, err := database.InsertChatID(db, m.Chat.ID, TypeChatid)
				if err != nil {
					return 0, fmt.Errorf("error  insert chatid to db %w", err)
				}
				if insert && TypeChatid == "govdao" {
					//for send ultimate Govdao
					log.Println("Send ultimate govdao telegram")
					govdaolist, err := database.GetLastGovDaoInfo(db)
					if err != nil {
						return 0, fmt.Errorf("error get lastid govdao%s ", err)

					}
					SendReportGovdaoTelegram(govdaolist.Id, govdaolist.Title, govdaolist.Url, govdaolist.Tx, m.Chat.ID)

				}

			}
		}
	}
	if last >= 0 {
		nextOffset = last + 1
	}
	return nextOffset, nil
}

func StartTelegramWatcher(botToken, chatType string, db *gorm.DB) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			next, err := GetChatIDs(botToken, chatType, db)
			if err != nil {
				log.Printf("❌ erreur GetChatIDs: %v", err)
				continue
			}
			log.Printf("✅ Check Telegram OK (nextOffset=%d)", next)
		}
	}
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

// ---- Boucle de commandes ----

// StartCommandLoop lit en continu getUpdates et appelle les handlers.
// - token: ton bot token
// - stopCtx: pour arrêter proprement (SIGINT/SIGTERM)
// - handlers: map "/status" -> func(chatID, args) ; "*" est un fallback
func StartCommandLoop(stopCtx context.Context, token string, handlers map[string]func(int64, string)) error {
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
			cmd, args, ok := extractCommand(up.Message)
			if !ok {
				continue
			}
			if h, found := handlers[cmd]; found {
				go h(up.Message.Chat.ID, args) // async pour ne pas bloquer
			} else if h, found := handlers["*"]; found {
				go h(up.Message.Chat.ID, cmd+" "+args)
			}
		}
	}
}
func parseParams(args string) map[string]string {
	out := map[string]string{}
	for _, tok := range strings.Fields(args) { // split par espaces
		// supporte "key=value" et "--key=value"
		tok = strings.TrimPrefix(tok, "--")
		kv := strings.SplitN(tok, "=", 2)
		if len(kv) == 2 {
			k := strings.ToLower(strings.TrimSpace(kv[0]))
			v := strings.Trim(kv[1], `"'`) // enlève guillemets éventuels
			out[k] = v
		}
	}
	return out
}

// BuildTelegramHandlers retourne la map de handlers
func BuildTelegramHandlers(token string, db *gorm.DB) map[string]func(int64, string) {
	return map[string]func(int64, string){

		"/status": func(chatID int64, args string) {
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = "current_month"
			}

			msg, err := formatParticipationRAte(db, period)
			if err != nil {
				log.Printf("error get particpate Rate%s", err)
			}

			_ = SendMessageTelegram(token, chatID, msg)
		},
		"/uptime": func(chatID int64, args string) {
			msg, err := formatUptime(db)
			if err != nil {
				log.Printf("error get uptime metrics: %s", err)
			}
			_ = SendMessageTelegram(token, chatID, msg)

		}, "/tx_contrib": func(chatID int64, args string) {
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = "current_month"
			}

			msg, err := FormatTxcontrib(db, period)
			if err != nil {
				log.Printf("error get tx_contribe%s", err)
			}

			_ = SendMessageTelegram(token, chatID, msg)
		},
		"/missing": func(chatID int64, args string) {
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = "current_month"
			}

			msg, err := formatMissing(db, period)
			if err != nil {
				log.Printf("error get missingg block%s", err)
			}

			_ = SendMessageTelegram(token, chatID, msg)
		},

		"/help": func(chatID int64, _ string) {

			msg := formatHelp()
			_ = SendMessageTelegram(token, chatID, msg)
		},

		"*": func(chatID int64, _ string) {
			_ = SendMessageTelegram(token, chatID,
				"Unknow command ❓ Essaie /help")
		},
	}
}

func formatParticipationRAte(db *gorm.DB, period string) (msg string, err error) {
	rates, err := database.GetCurrentPeriodParticipationRate(db, period)
	if err != nil {
		return "", fmt.Errorf("failed to get participation rate: %v", err)

	}
	if len(rates) == 0 {
		return fmt.Sprintf("📊 <b>Participation rates — %s</b>\nNo data.", html.EscapeString(period)), nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("📊 <b>Participation rates for %s</b>\n\n", period))

	for _, r := range rates {

		emoji := "🟢"
		if r.ParticipationRate < 95.0 {
			emoji = "🟡"
		}
		if r.ParticipationRate < 70.0 {
			emoji = "🟠"

		}
		if r.ParticipationRate < 50.0 {
			emoji = "🔴"

		}
		builder.WriteString(fmt.Sprintf(
			"%s  <b>%s <b> \n addr:  %s \n %.2f%%\n\n",
			emoji, html.EscapeString(r.Moniker), r.Addr, r.ParticipationRate,
		))

	}

	return builder.String(), nil
}
func formatUptime(db *gorm.DB) (msg string, err error) {

	results, err := database.UptimeMetricsaddr(db)
	if err != nil {
		return "", fmt.Errorf("failed to get uptime metrics: %v", err)

	}
	var builder strings.Builder
	builder.WriteString("🕘 <b>Uptime metrics </b>\n\n")
	for _, r := range results {
		builder.WriteString(fmt.Sprintf(
			"<b> %s <b> \n addr: %s \n uptime : %.2f%% days \n\n",
			html.EscapeString(r.Moniker), r.Addr, r.DaysDiff,
		))

	}

	return builder.String(), err
}
func FormatTxcontrib(db *gorm.DB, period string) (msg string, err error) {
	txcontrib, err := database.TxContrib(db, period)
	if err != nil {
		return "", fmt.Errorf("failed to get tx_contrib: %v", err)

	}
	if len(txcontrib) == 0 {
		return fmt.Sprintf("⚙️ <b>Tx Contrib — %s</b>\nNo data.", html.EscapeString(period)), nil
	}
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("⚙️ <b>Tx Contrib metrics for %s</b>\n\n", period))

	for _, r := range txcontrib {

		builder.WriteString(fmt.Sprintf(
			"<b> %s <b> \n addr %s  \n contrib : %.2f%%\n\n",
			html.EscapeString(r.Moniker), r.Addr, r.TxContrib,
		))

	}

	return builder.String(), nil
}

func formatMissing(db *gorm.DB, period string) (string, error) {
	rows, err := database.MissingBlock(db, period)
	if err != nil {
		return "", fmt.Errorf("failed to get missing block: %w", err)
	}
	if len(rows) == 0 {
		return fmt.Sprintf("🕵️ <b>Missing blocks — %s</b>\nNo data.", html.EscapeString(period)), nil
	}

	// Trier par blocs manqués (desc)
	sort.Slice(rows, func(i, j int) bool { return rows[i].MissingBlock > rows[j].MissingBlock })

	var b strings.Builder

	b.WriteString(fmt.Sprintf("🕵️ <b>Missing Blocks — %s</b>\n\n", html.EscapeString(period)))
	for i, r := range rows {
		emoji := "🟢"
		if r.MissingBlock > 50 {
			emoji = "🔴"
		} else if r.MissingBlock > 20 {
			emoji = "🟠"
		} else if r.MissingBlock > 5 {
			emoji = "🟡"
		}

		b.WriteString(fmt.Sprintf(
			"%d. %s <b>%s</b>\n<code>%s</code>\nMissing: <b>%d blocks</b> \n\n",
			i+1, emoji, html.EscapeString(r.Moniker), r.Addr, r.MissingBlock,
		))
	}
	return b.String(), nil
}

func formatHelp() string {
	var b strings.Builder
	b.WriteString("🆘 <b>Help</b>\n\n")

	b.WriteString("⏱️ <b>Available periods</b>\n")
	b.WriteString("• <code>current_week</code>\n")
	b.WriteString("• <code>current_month</code>\n")
	b.WriteString("• <code>current_year</code>\n")
	b.WriteString("• <code>all_time</code>\n\n")

	b.WriteString("📡 <b>Commands</b>\n")

	b.WriteString("<code>/status [period=...]</code>\n")
	b.WriteString("Shows the participation rate of validators for a given period.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/status</code> (default: current_week)\n")
	b.WriteString("• <code>/status period=current_month</code>\n\n")

	b.WriteString("<code>/uptime</code>\n")
	b.WriteString("Displays uptime statistics of validator.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/uptime</code>\n\n")

	b.WriteString("<code>/tx_contrib [period=...]</code>\n")
	b.WriteString("Shows each validator’s contribution to transaction inclusion.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/tx_contrib</code>\n")
	b.WriteString("• <code>/tx_contrib period=current_year</code>\n\n")

	b.WriteString("<code>/missing [period=...]</code>\n")
	b.WriteString("Displays how many blocks each validator missed for a given period.\n")
	b.WriteString("Examples:\n")
	b.WriteString("• <code>/missing</code>\n")
	b.WriteString("• <code>/missing period=all_time</code>\n\n")

	b.WriteString("ℹ️ Parameters must be written as <code>key=value</code> (e.g. <code>period=current_week</code>).\n")

	return b.String()
}
