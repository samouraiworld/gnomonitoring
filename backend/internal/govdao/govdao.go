package govdao

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
)

var runningWatchers = make(map[string]bool)
var mu sync.Mutex

func StartGovDaoManager(db *sql.DB) {
	log.Println("GovDao manager started")
	ticker := time.NewTicker(10 * time.Second) // check every 10s
	defer ticker.Stop()

	for range ticker.C {
		webhooks, err := database.Loadwebhooks(db)
		if err != nil {
			log.Printf("Erreur chargement des webhooks: %v", err)
			continue
		}

		mu.Lock()
		for _, wh := range webhooks {
			if !runningWatchers[wh.URL] {
				log.Printf("🔁 Nouvelle surveillance GovDAO pour %s", wh.URL)
				go StartWebhookWatcher(wh, db)
				runningWatchers[wh.URL] = true
			}
		}
		mu.Unlock()
	}
}

func StartWebhookWatcher(w database.WebhookGovDao, db *sql.DB) {
	// log.Println("Begin Start GovDao")
	ticker := time.NewTicker(time.Duration(internal.Config.IntervallSecond) * time.Second)
	defer ticker.Stop()
	// log.Printf("user %s url:%s", w.USER, w.URL)
	for range ticker.C {

		nextID := w.LastCheckedID + 1
		exists, title, moniker := ProposalExists(nextID)
		log.Printf("check GovDao num %d\n", nextID)

		if exists {
			msg := fmt.Sprintf("--- \n 🗳️ ** New Proposal N° %d: %s ** - %s \n 🔗source: %s/r/gov/dao:%d",
				nextID, title, moniker, internal.Config.Gnoweb, nextID)

			msgSlack := fmt.Sprintf("--- \n 🗳️*New Proposal N° %d: %s* - %s_\n🔗source: %s/r/gov/dao:%d",
				nextID, title, moniker, internal.Config.Gnoweb, nextID)

			switch w.Type {
			case "discord":
				log.Println("Send GovDao alert")
				internal.SendDiscordAlert(msg, w.URL)
			case "slack":
				internal.SendSlackAlert(msgSlack, w.URL)
			}

			database.UpdateLastCheckedID(w.URL, nextID, db)
			w.LastCheckedID = nextID
		}
		database.UpdateLastGovDaoProposalID(db, nextID-1)
	}

}
func ProposalExists(i int) (bool, string, string) {
	url := fmt.Sprintf("%s/r/gov/dao:%d", internal.Config.Gnoweb, i)
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
