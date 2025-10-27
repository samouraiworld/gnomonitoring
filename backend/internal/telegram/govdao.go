package telegram

import (
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

func BuildTelegramGovdaoHandlers(token string, db *gorm.DB) map[string]func(int64, string) {
	period_default := "current_month"

	limit_default := int64(10)
	return map[string]func(int64, string){
		// status on Govdao proposals
		"/status": func(chatID int64, args string) {
			params := parseParams(args)
			period := params["period"]
			if period == "" {
				period = period_default
			}

			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit value : %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatParticipationRAte(db, period, limitint)
			if err != nil {
				log.Printf("error get particpate Rate%s", err)
			}

			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/status", err)
			}

		},
		// last executed proposals
		"/executedproposal": func(chatID int64, args string) {
			params := parseParams(args)
			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit: %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatUptime(db, limitint)
			if err != nil {
				log.Printf("error get uptime metrics: %s", err)
			}
			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/uptime", err)
			}
			// last proposal posted
		}, "/lastproposal": func(chatID int64, args string) {
			params := parseParams(args)
			limit, err := strconv.ParseInt(params["limit"], 10, 64)
			if err != nil {
				log.Printf("error conversion limit: %v", err)
				limit = limit_default
			}
			limitint := int(limit)

			msg, err := formatOperationTime(db, limitint)
			if err != nil {
				log.Printf("error get uptime metrics: %s", err)
			}
			if err := SendMessageTelegram(token, chatID, msg); err != nil {
				log.Printf("send %s failed: %v", "/uptime", err)
			}

		},

		"/help": func(chatID int64, _ string) {

			msg := formatHelp()
			_ = SendMessageTelegram(token, chatID, msg)
		},

		"*": func(chatID int64, _ string) {
			if err := SendMessageTelegram(token, chatID,
				"Unknown command ❓ try /help"); err != nil {
				log.Printf("send %s failed: %v", "/status", err)
			}
		},
	}
}

func formatStatusProposal(db *gorm.DB, limit int) (msg string, err error) {

	status, err := database.GetStatusofGovdao(db)
	if err != nil {
		return "", fmt.Errorf("failed to get status govdao : %v", err)

	}
	if len(status) == 0 {
		return " <b>There is no govdao available", nil
	}
	var builder strings.Builder
	builder.WriteString("🗳️ <b>Gov Dao Proposal %s</b>")

	// limit
	if len(status) < limit {
		limit = len(status)
	}

	for i, r := range status {

		if i >= limit {
			break
		}

		builder.WriteString(fmt.Sprintf(
			"%s  <b>%s </b> \n addr:  %s \n %.2f%%\n\n",
			emoji, html.EscapeString(r.Moniker), html.EscapeString(r.Addr), r.ParticipationRate,
		))

	}

	return msg, nil
}

func SendReportGovdaoTelegram(id int, title, urlgnoweb, urltx, botoken string, chatid int64) error {
	msg := FormatTelegramMsg(id, title, urlgnoweb, urltx)

	err := SendMessageTelegram(botoken, chatid, msg)
	if err != nil {
		log.Printf("error send govdao telegram  %s", err)
	}

	return nil

}
func FormatTelegramMsg(id int, title, proposalURL, txURL string) string {
	esc := html.EscapeString
	voteURL := fmt.Sprintf("https://gnolove.world/govdao/proposal/%d", id)

	return fmt.Sprintf(

		"🗳️ <b>New Proposal Nº %d</b>: %s\n"+
			"🔗 Source: <a href=\"%s\">Gno.land</a>\n"+
			"🗒️ Tx: <a href=\"%s\">Gnoscan</a>\n"+
			"🖐️ Interact & Vote: <a href=\"%s\">Open proposal on Gnolove</a>",
		id,
		esc(title),
		esc(proposalURL),
		esc(txURL),
		esc(voteURL),
	)
}
