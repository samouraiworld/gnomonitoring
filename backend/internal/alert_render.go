package internal

import (
	"fmt"
	"html"
	"strings"
)

// AlertLevel classifies the severity/kind of an alert, driving the color
// used by channel renderers.
type AlertLevel string

const (
	AlertCritical AlertLevel = "CRITICAL"
	AlertWarning  AlertLevel = "WARNING"
	AlertResolved AlertLevel = "RESOLVED"
	AlertInfo     AlertLevel = "INFO"
)

// AlertField is one labeled fact shown in an alert (e.g. "addr" -> "g1...").
type AlertField struct {
	Name  string
	Value string
}

// AlertData is the channel-neutral content of one alert/notification. Each
// call site builds exactly the same values it computed before this
// struct existed; only the container changed from a formatted string to
// structured fields, so no channel renderer needs to guess at meaning.
type AlertData struct {
	ChainID string
	Level   AlertLevel
	Emoji   string // e.g. 🚨 ⚠️ ✅ 🔄

	Title string

	// Description is a free-text sentence for alerts with no natural
	// key/value facts (e.g. "Gno.land is back to normal."). Alerts that
	// use Fields leave Description empty, and vice versa.
	Description string

	// Date is optional trailing date/context, rendered as the Discord
	// embed footer and a trailing Telegram line.
	Date string

	Fields []AlertField

	// Mentions holds raw Discord/Slack mention tags (as stored in
	// AlertContact). Empty for most alerts (only CRITICAL missed-block
	// alerts populate it today).
	Mentions []string
}

const (
	alertColorCritical = 0xE74C3C
	alertColorWarning  = 0xF39C12
	alertColorResolved = 0x2ECC71
	alertColorInfo      = 0x3498DB
)

func alertColor(level AlertLevel) int {
	switch level {
	case AlertCritical:
		return alertColorCritical
	case AlertWarning:
		return alertColorWarning
	case AlertResolved:
		return alertColorResolved
	default:
		return alertColorInfo
	}
}

// RenderAlertDiscordEmbed builds a Discord embed for d, plus a separate
// plain-text content string carrying any @mentions — Discord does not
// parse mentions written inside an embed's title/description/fields, so
// they must live in the top-level message content instead.
func RenderAlertDiscordEmbed(d AlertData) (content string, embed DiscordEmbed) {
	if len(d.Mentions) > 0 {
		lines := make([]string, len(d.Mentions))
		for i, m := range d.Mentions {
			lines[i] = "<@" + m + ">"
		}
		content = strings.Join(lines, "\n")
	}

	fields := make([]DiscordEmbedField, len(d.Fields))
	for i, f := range d.Fields {
		fields[i] = DiscordEmbedField{Name: f.Name, Value: f.Value}
	}

	embed = DiscordEmbed{
		Title:       fmt.Sprintf("[%s] %s %s", d.ChainID, d.Emoji, d.Title),
		Description: d.Description,
		Color:       alertColor(d.Level),
		Fields:      fields,
	}
	if d.Date != "" {
		embed.Footer = &DiscordEmbedFooter{Text: d.Date}
	}
	return content, embed
}

// RenderAlertSlackBlocks builds a Slack Block Kit message for d: a header,
// a single section combining Description (if any) and one "*Name*: Value"
// line per Field, and a context block with mentions when present.
func RenderAlertSlackBlocks(d AlertData) []SlackBlock {
	blocks := []SlackBlock{
		{
			Type: "header",
			Text: &SlackText{Type: "plain_text", Text: fmt.Sprintf("[%s] %s %s", d.ChainID, d.Emoji, d.Title)},
		},
	}

	var lines []string
	if d.Description != "" {
		lines = append(lines, d.Description)
	}
	for _, f := range d.Fields {
		lines = append(lines, fmt.Sprintf("*%s*: %s", f.Name, f.Value))
	}
	if len(lines) > 0 {
		blocks = append(blocks, SlackBlock{
			Type: "section",
			Text: &SlackText{Type: "mrkdwn", Text: strings.Join(lines, "\n")},
		})
	}

	if d.Date != "" {
		blocks = append(blocks, SlackBlock{
			Type:     "context",
			Elements: []SlackText{{Type: "mrkdwn", Text: d.Date}},
		})
	}

	if len(d.Mentions) > 0 {
		mentionText := make([]string, len(d.Mentions))
		for i, m := range d.Mentions {
			mentionText[i] = "<@" + m + ">"
		}
		blocks = append(blocks, SlackBlock{
			Type:     "context",
			Elements: []SlackText{{Type: "mrkdwn", Text: strings.Join(mentionText, " ")}},
		})
	}
	return blocks
}

// RenderAlertTelegramHTML formats d as an HTML message body for Telegram's
// parse_mode=HTML. Every interpolated value is HTML-escaped. Field values
// whose name mentions "addr" render in <code> (matches the existing
// convention for addresses in the daily report renderer).
func RenderAlertTelegramHTML(d AlertData) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("<b>[%s] %s %s</b>\n",
		html.EscapeString(d.ChainID), html.EscapeString(d.Emoji), html.EscapeString(d.Title)))

	if d.Description != "" {
		sb.WriteString(html.EscapeString(d.Description) + "\n")
	}

	for _, f := range d.Fields {
		name := html.EscapeString(f.Name)
		value := html.EscapeString(f.Value)
		if strings.Contains(strings.ToLower(f.Name), "addr") {
			sb.WriteString(fmt.Sprintf("<b>%s</b>: <code>%s</code>\n", name, value))
		} else {
			sb.WriteString(fmt.Sprintf("<b>%s</b>: %s\n", name, value))
		}
	}

	if d.Date != "" {
		sb.WriteString(html.EscapeString(d.Date) + "\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}
