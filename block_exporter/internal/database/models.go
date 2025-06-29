package database

type User struct {
	ID       uint `gorm:"primaryKey"`
	Username string
	Email    string
	Webhooks []Webhook
}

type Webhook struct {
	ID       uint `gorm:"primaryKey"`
	URL      string
	UserID   uint
	Active   bool
	Contacts []Contact
}

type Contact struct {
	ID        uint `gorm:"primaryKey"`
	Name      string
	WebhookID uint
}
