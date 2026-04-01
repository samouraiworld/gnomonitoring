package database

import (
	"fmt"
	"strconv"

	"gorm.io/gorm"
)

// GetAdminConfig returns the string value for a given admin_config key.
func GetAdminConfig(db *gorm.DB, key string) (string, error) {
	var cfg AdminConfig
	if err := db.Where("key = ?", key).First(&cfg).Error; err != nil {
		return "", fmt.Errorf("GetAdminConfig %q: %w", key, err)
	}
	return cfg.Value, nil
}

// GetAdminConfigInt returns the integer value for a given admin_config key.
// Returns fallback if the key is missing or not a valid integer.
func GetAdminConfigInt(db *gorm.DB, key string, fallback int) int {
	val, err := GetAdminConfig(db, key)
	if err != nil {
		return fallback
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return n
}

// SetAdminConfig upserts a key/value pair in admin_config.
func SetAdminConfig(db *gorm.DB, key, value string) error {
	return db.Exec(
		"INSERT INTO admin_configs (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	).Error
}
