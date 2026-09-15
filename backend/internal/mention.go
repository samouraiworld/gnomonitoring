package internal

import (
	"errors"
	"strings"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"gorm.io/gorm"
)

// roleMentionPrefix marks a stored mention tag as a Discord role ID rather
// than a user ID. A snowflake alone does not say what it identifies, so the
// type has to be carried by the tag itself: "123" is a user, "&123" a role —
// the same "&" Discord uses inside a role mention (<@&123>).
const roleMentionPrefix = "&"

var ErrInvalidMentionTag = errors.New("invalid mention_tag: must be a numeric Discord user ID, or &<id> for a role (pasting <@id> or <@&id> also works)")

// NormalizeMentionTag validates a user-supplied mention tag and returns its
// canonical stored form: "" (no ping), "<id>" (user) or "&<id>" (role). It
// also accepts the full mention syntax Discord shows when a mention is
// escaped in chat (\@name → <@id>, <@!id>, <@&id>), so operators can paste
// it as-is.
func NormalizeMentionTag(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "<@") && strings.HasSuffix(s, ">") {
		s = s[2 : len(s)-1]
		// <@!id> is Discord's legacy nickname form of a user mention.
		s = strings.TrimPrefix(s, "!")
	}

	prefix := ""
	if strings.HasPrefix(s, roleMentionPrefix) {
		prefix = roleMentionPrefix
		s = s[len(roleMentionPrefix):]
	}
	if s == "" {
		return "", ErrInvalidMentionTag
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return "", ErrInvalidMentionTag
		}
	}
	return prefix + s, nil
}

func isRoleMention(tag string) bool {
	return strings.HasPrefix(tag, roleMentionPrefix)
}

// DiscordAllowedMentions is Discord's allowed_mentions payload object.
// Parse is always sent as an empty (non-nil) list so Discord pings exactly
// the listed IDs and nothing parsed from the content (e.g. @everyone).
type DiscordAllowedMentions struct {
	Parse []string `json:"parse"`
	Users []string `json:"users,omitempty"`
	Roles []string `json:"roles,omitempty"`
}

// DiscordAllowedMentionsFor splits stored mention tags into the user and
// role ID lists Discord expects.
func DiscordAllowedMentionsFor(mentions []string) DiscordAllowedMentions {
	am := DiscordAllowedMentions{Parse: []string{}}
	for _, m := range mentions {
		if isRoleMention(m) {
			am.Roles = append(am.Roles, strings.TrimPrefix(m, roleMentionPrefix))
		} else {
			am.Users = append(am.Users, m)
		}
	}
	return am
}

// mentionsForAlert returns the mention tags of the alert contacts attached
// to (userID, moniker, webhookID). Only WARNING and CRITICAL validator alerts
// on Discord/Slack webhooks carry mentions; contacts with an empty tag are
// skipped since there is nobody to ping.
func mentionsForAlert(db *gorm.DB, level, webhookType, userID, moniker string, webhookID int) ([]string, error) {
	if level != "CRITICAL" && level != "WARNING" {
		return nil, nil
	}
	if webhookType != "discord" && webhookType != "slack" {
		return nil, nil
	}
	var tags []string
	if err := db.Model(&database.AlertContact{}).
		Where("user_id = ? AND moniker = ? AND id_webhook = ? AND mention_tag <> ''", userID, moniker, webhookID).
		Order("id ASC").
		Pluck("mention_tag", &tags).Error; err != nil {
		return nil, err
	}
	return tags, nil
}
