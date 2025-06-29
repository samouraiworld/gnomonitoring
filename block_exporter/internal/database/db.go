package database

import (
	"log"
	"sync"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var (
	db   *gorm.DB
	once sync.Once
)

func InitDB() *gorm.DB {
	once.Do(func() {
		var err error
		db, err = gorm.Open(sqlite.Open("gnomonitoring.db"), &gorm.Config{})
		if err != nil {
			log.Fatalf("failed to connect database: %v", err)
		}

		// Migration auto
		err = db.AutoMigrate(&User{}, &Webhook{}, &Contact{})
		if err != nil {
			log.Fatalf("auto migration failed: %v", err)
		}
	})

	return db
}
