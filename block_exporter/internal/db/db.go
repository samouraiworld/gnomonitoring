package db

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

func InitDB() {
	database, err := gorm.Open(sqlite.Open("gnomonitor.db"), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}
	DB = database

	// Auto migrate models (à créer ensuite)
	database.AutoMigrate(&User{}, &Contact{}, &ValidatorMonitor{})
}
