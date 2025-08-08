package govdao

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

var runningWatchers = make(map[string]bool)
var mu sync.Mutex

func StartGovDaoManager(db *gorm.DB) {
	log.Println("GovDao manager started")
	ticker := time.NewTicker(10 * time.Minute) // check every 10s
	defer ticker.Stop()

	for range ticker.C {
		webhooks, err := database.LoadWebhooks(db)
		if err != nil {
			log.Printf("Error loading webhooks: %v", err)
			continue
		}

		mu.Lock()
		for _, wh := range webhooks {
			key := fmt.Sprintf("%s|%s", wh.UserID, wh.URL)
			if !runningWatchers[key] {
				//	log.Printf("🔁 New GovDAO Surveillance for %s", wh.URL)
				go StartWebhookWatcher(wh, db)
				runningWatchers[wh.URL] = true
			}
		}
		mu.Unlock()
	}
}

func StartWebhookWatcher(w database.WebhookGovDAO, db *gorm.DB) {
	log.Println("Begin Start GovDao")
	ticker := time.NewTicker(time.Duration(internal.Config.IntervallSecond) * time.Second)
	defer ticker.Stop()

	for range ticker.C {

		nextID := w.LastCheckedID + 1
		exists, title, moniker := ProposalExists(nextID)

		if exists {
			msg := fmt.Sprintf("--- \n 🗳️ ** New Proposal N° %d: %s ** - %s \n 🔗source: %s/r/gov/dao:%d",
				nextID, title, moniker, internal.Config.Gnoweb, nextID)

			msgSlack := fmt.Sprintf("--- \n 🗳️*New Proposal N° %d: %s* - %s_\n🔗source: %s/r/gov/dao:%d",
				nextID, title, moniker, internal.Config.Gnoweb, nextID)

			switch w.Type {
			case "discord":

				internal.SendDiscordAlert(msg, w.URL)
			case "slack":
				internal.SendSlackAlert(msgSlack, w.URL)
			}

			database.UpdateLastCheckedID(w.URL, nextID, db)
			database.UpdateLastGovDaoProposalID(db, nextID-1)
			w.LastCheckedID = nextID
		}

	}

}
func ProposalExists(i int) (bool, string, string) {
	url := fmt.Sprintf("%s/r/gov/dao:%d", internal.Config.Gnoweb, i)
	//println(url)
	resp, err := http.Get(url)

	if err != nil {
		fmt.Printf("Error HTTP : %v\n", err)
		return false, "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", ""
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		fmt.Printf("Error parsing HTML : %v\n", err)
		return true, "", ""
	}

	title := strings.TrimPrefix(doc.Find("h3[id]").Eq(0).Text(), "Title: ")

	moniker := doc.Find("h2[id]").Eq(1).Text()
	println(title, moniker)
	return true, title, moniker
}
