package scheduler

import (
	"database/sql"
	"fmt"
	"log"
	"sync"

	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
)

type Scheduler struct {
	db          *sql.DB
	reloadChans map[string]chan struct{}
	mu          sync.Mutex
}

var Schedulerinstance *Scheduler

func InitScheduler(db *sql.DB) {
	Schedulerinstance = &Scheduler{
		db:          db,
		reloadChans: make(map[string]chan struct{}),
	}
	Schedulerinstance.StartAll(db)
	println("Scheduler started")
}

func (s *Scheduler) StartAll(db *sql.DB) {
	rows, err := s.db.Query("SELECT user_id, daily_report_hour, daily_report_minute, timezone FROM hour_report")
	if err != nil {
		log.Fatalf("❌ Failed to fetch report hours: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var userID, tz string
		var hour, minute int
		if err := rows.Scan(&userID, &hour, &minute, &tz); err != nil {
			log.Printf("⚠️ Error scanning user report config: %v", err)
			continue
		}
		s.StartForUser(userID, hour, minute, tz, db)
	}
}
func (s *Scheduler) StartForUser(userID string, hour, minute int, timezone string, db *sql.DB) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Si un scheduler existe déjà : on le "kill"
	if ch, exists := s.reloadChans[userID]; exists {
		close(ch) // envoie un signal d’arrêt
	}

	// Nouveau canal de reload
	reload := make(chan struct{})
	s.reloadChans[userID] = reload

	go func() {
		gnovalidator.SheduleUserReport(userID, hour, minute, timezone, db, reload)
	}()
}

func (s *Scheduler) ReloadForUser(userID string, db *sql.DB) error {
	var hour, minute int
	var tz string

	err := s.db.QueryRow(`SELECT daily_report_hour, daily_report_minute, timezone FROM hour_report WHERE user_id = ?`, userID).
		Scan(&hour, &minute, &tz)

	if err != nil {
		return fmt.Errorf("failed to reload user config: %w", err)
	}

	s.StartForUser(userID, hour, minute, tz, db)
	return nil
}
