package internal

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

func StartWebhookWatcher(w database.WebhookGovDao, db *sql.DB) {
	log.Println("Begin Start GovDao")
	ticker := time.NewTicker(time.Duration(Config.IntervallSecond) * time.Second)
	defer ticker.Stop()
	log.Printf("user %s url:%s", w.USER, w.URL)
	for range ticker.C {
		nextID := w.LastCheckedID + 1
		exists, title, moniker := ProposalExists(nextID)
		log.Printf("check GovDao num %d\n", nextID)

		if exists {
			msg := fmt.Sprintf("--- \n 🗳️ ** New Proposal N° %d: %s ** - %s \n 🔗source: %s/r/gov/dao:%d",
				nextID, title, moniker, Config.Gnoweb, nextID)

			msgSlack := fmt.Sprintf("--- \n 🗳️*New Proposal N° %d: %s* - %s_\n🔗source: %s/r/gov/dao:%d",
				nextID, title, moniker, Config.Gnoweb, nextID)

			switch w.Type {
			case "discord":
				log.Println("Send GovDao alert")
				SendDiscordAlert(msg, w.URL)
			case "slack":
				SendSlackAlert(msgSlack, w.URL)
			}

			database.UpdateLastCheckedID(w.URL, nextID, db)
			w.LastCheckedID = nextID
		}
	}

	// for {
	// 	select {
	// 	case <-ticker.C:
	// 		nextID := w.LastCheckedID + 1
	// 		exists, title, moniker := ProposalExists(nextID)
	// 		log.Printf("check GovDao num %d\n", nextID)
	// 		if exists {
	// 			msg := fmt.Sprintf("--- \n 🗳️ ** New Proposal N° %d: %s ** - %s \n 🔗source: %s/r/gov/dao:%d", nextID, title, moniker, Config.Gnoweb, nextID)

	// 			msgSlack := fmt.Sprintf("--- \n 🗳️*New Proposal N° %d: %s* - %s_\n🔗source: %s/r/gov/dao:%d", nextID, title, moniker, Config.Gnoweb, nextID)
	// 			// msg := fmt.Sprintf("--- \n 🗳️ ** New Proposal N° %d: %s ** - %s \n 🔗source: https://test7.testnets.gno.land/r/gov/dao:%d  ", nextID, title, moniker, nextID)
	// 			// msgSlack := fmt.Sprintf("--- \n 🗳️*New Proposal N° %d: %s* - %s_\n🔗source: https://test7.testnets.gno.land/r/gov/dao:%d  ", nextID, title, moniker, nextID)
	// 			switch w.Type {
	// 			case "discord":
	// 				log.Println("Send GovDao alert")
	// 				SendDiscordAlert(msg, w.URL)
	// 			case "slack":
	// 				SendSlackAlert(msgSlack, w.URL)
	// 			}

	// 			UpdateLastCheckedID(w.URL, nextID, db)
	// 			w.LastCheckedID = nextID
	// 		}
	// 	}
	// }
}
func ProposalExists(i int) (bool, string, string) {
	url := fmt.Sprintf("%s/r/gov/dao:%d", Config.Gnoweb, i)
	resp, err := http.Get(url)
	if err != nil {
		fmt.Printf("Erreur HTTP : %v\n", err)
		return false, "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", ""
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		fmt.Printf("Erreur parsing HTML : %v\n", err)
		return true, "", ""
	}

	title := strings.TrimPrefix(doc.Find("h3[id]").Eq(0).Text(), "Title: ")

	moniker := doc.Find("h2[id]").Eq(1).Text()

	return true, title, moniker
}
