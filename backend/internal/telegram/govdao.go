package telegram

import (
	"fmt"
	"html"
	"log"
)

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
