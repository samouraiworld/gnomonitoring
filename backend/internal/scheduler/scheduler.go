package scheduler

import (
	"fmt"
	"log"
	"sync"

	"github.com/samouraiworld/gnomonitoring/backend/internal/gnovalidator"
	"gorm.io/gorm"
)

type Scheduler struct {
	db          *gorm.DB
	reloadChans map[string]chan struct{}
	mu          sync.Mutex
}

var Schedulerinstance *Scheduler

func InitScheduler(db *gorm.DB) {
	Schedulerinstance = &Scheduler{
		db:          db,
		reloadChans: make(map[string]chan struct{}),
	}
	Schedulerinstance.StartAll(db)
	println("Scheduler started")
}

func (s *Scheduler) StartAll(db *gorm.DB) {
	rows, err := db.Raw(`
		SELECT user_id, daily_report_hour, daily_report_minute, timezone 
		FROM hour_reports
	`).Rows()
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
func (s *Scheduler) StartForUser(userID string, hour, minute int, timezone string, db *gorm.DB) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If a scheduler already exists: we "kill" it
	if ch, exists := s.reloadChans[userID]; exists {
		close(ch) // sends a stop signal
	}

	// New reload channel
	reload := make(chan struct{})
	s.reloadChans[userID] = reload

	go func() {
		gnovalidator.SheduleUserReport(userID, hour, minute, timezone, db, reload)
	}()
}

func (s *Scheduler) ReloadForUser(userID string, db *gorm.DB) error {
	var config struct {
		DailyReportHour   int
		DailyReportMinute int
		Timezone          string
	}
	err := s.db.Raw(`
		SELECT daily_report_hour, daily_report_minute, timezone 
		FROM hour_reports 
		WHERE user_id = ?
	`, userID).Scan(&config).Error

	if err != nil {
		return fmt.Errorf("failed to reload user config: %w", err)
	}
	s.StartForUser(userID, config.DailyReportHour, config.DailyReportMinute, config.Timezone, db)
	return nil

}
