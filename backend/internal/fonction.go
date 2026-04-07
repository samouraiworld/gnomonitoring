package internal

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/telegram"
	"gopkg.in/yaml.v2"
	"gorm.io/gorm"
)

// ConfigMu protects mutations to Config and EnabledChains from the admin API.
// Existing readers (monitoring goroutines) do not hold this lock; mutations are
// infrequent enough that eventual consistency is acceptable.
var ConfigMu sync.Mutex

type ChainConfig struct {
	RPCEndpoints     []string `yaml:"rpc_endpoints"`
	GraphqlEndpoints []string `yaml:"graphqls"`
	GnowebEndpoints  []string `yaml:"gnowebs"`
	Enabled          bool     `yaml:"enabled"`
}

func (c *ChainConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var raw struct {
		RPCEndpoint      string   `yaml:"rpc_endpoint"`
		RPCEndpoints     []string `yaml:"rpc_endpoints"`
		GraphqlEndpoint  string   `yaml:"graphql"`
		GraphqlEndpoints []string `yaml:"graphqls"`
		GnowebEndpoint   string   `yaml:"gnoweb"`
		GnowebEndpoints  []string `yaml:"gnowebs"`
		Enabled          bool     `yaml:"enabled"`
	}
	if err := unmarshal(&raw); err != nil {
		return err
	}
	c.Enabled = raw.Enabled
	if len(raw.RPCEndpoints) > 0 {
		c.RPCEndpoints = raw.RPCEndpoints
	} else if raw.RPCEndpoint != "" {
		c.RPCEndpoints = []string{raw.RPCEndpoint}
	}
	if len(raw.GraphqlEndpoints) > 0 {
		c.GraphqlEndpoints = raw.GraphqlEndpoints
	} else if raw.GraphqlEndpoint != "" {
		c.GraphqlEndpoints = []string{raw.GraphqlEndpoint}
	}
	if len(raw.GnowebEndpoints) > 0 {
		c.GnowebEndpoints = raw.GnowebEndpoints
	} else if raw.GnowebEndpoint != "" {
		c.GnowebEndpoints = []string{raw.GnowebEndpoint}
	}
	return nil
}

func (c *ChainConfig) RPCEndpoint() string {
	if len(c.RPCEndpoints) == 0 {
		return ""
	}
	return c.RPCEndpoints[0]
}

func (c *ChainConfig) GraphqlEndpoint() string {
	if len(c.GraphqlEndpoints) == 0 {
		return ""
	}
	return c.GraphqlEndpoints[0]
}

func (c *ChainConfig) GnowebEndpoint() string {
	if len(c.GnowebEndpoints) == 0 {
		return ""
	}
	return c.GnowebEndpoints[0]
}

type config struct {
	BackendPort            string                  `yaml:"backend_port"`
	AllowOrigin            string                  `yaml:"allow_origin"`
	MetricsPort            int                     `yaml:"metrics_port"`
	ClerkSecretKey         string                  `yaml:"clerk_secret_key"`
	DevMode                bool                    `yaml:"dev_mode"`
	TokenTelegramValidator string                  `yaml:"token_telegram_validator"`
	TokenTelegramGovdao    string                  `yaml:"token_telegram_govdao"`
	Chains                 map[string]*ChainConfig `yaml:"chains"`
	DefaultChain           string                  `yaml:"default_chain"`

	// Parsed at load time from AllowOrigin (comma-separated).
	AllowedOrigins []string `yaml:"-"`
}

var Config config

// EnabledChains holds the IDs of all chains with Enabled: true, sorted alphabetically.
var EnabledChains []string

// alertHTTPClient is reused across all webhook dispatches to enable TCP connection pooling.
var alertHTTPClient = &http.Client{Timeout: 10 * time.Second}

// LoadConfig reads config.yaml, validates chains, and initialises EnabledChains.
func LoadConfig() {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	err = yaml.Unmarshal(data, &Config)
	if err != nil {
		log.Fatalf("Error parsing config file: %v", err)
	}

	if len(Config.Chains) == 0 {
		log.Fatalf("Config error: no chains defined under 'chains:'")
	}

	// Build EnabledChains sorted alphabetically.
	for id, chain := range Config.Chains {
		if chain.Enabled {
			EnabledChains = append(EnabledChains, id)
		}
	}
	sort.Strings(EnabledChains)
	log.Printf("Enabled chains: %v", EnabledChains)

	// Validate default_chain: it must exist in Chains and be enabled.
	// Fall back to EnabledChains[0] if not set or invalid.
	if Config.DefaultChain != "" {
		chain, ok := Config.Chains[Config.DefaultChain]
		if !ok {
			log.Printf("Config warning: default_chain %q not found in chains, falling back to %q", Config.DefaultChain, EnabledChains[0])
			Config.DefaultChain = EnabledChains[0]
		} else if !chain.Enabled {
			log.Printf("Config warning: default_chain %q is not enabled, falling back to %q", Config.DefaultChain, EnabledChains[0])
			Config.DefaultChain = EnabledChains[0]
		}
	} else if len(EnabledChains) > 0 {
		Config.DefaultChain = EnabledChains[0]
	}
	log.Printf("Default chain: %v", Config.DefaultChain)

	// Parse comma-separated origins into a slice for dynamic CORS matching.
	for _, raw := range strings.Split(Config.AllowOrigin, ",") {
		origin := strings.TrimSpace(raw)
		if origin != "" {
			Config.AllowedOrigins = append(Config.AllowedOrigins, origin)
		}
	}

	log.Printf("DevMode value: %v", Config.DevMode)
	log.Printf("Allowed CORS origins: %v", Config.AllowedOrigins)
}

// GetChainConfig returns the ChainConfig for chainID, or an error if not found.
func (c *config) GetChainConfig(chainID string) (*ChainConfig, error) {
	chain, ok := c.Chains[chainID]
	if !ok {
		return nil, fmt.Errorf("unknown chain ID: %q", chainID)
	}
	return chain, nil
}

// ValidateChainID returns an error if chainID is not present in Chains.
func (c *config) ValidateChainID(chainID string) error {
	if _, ok := c.Chains[chainID]; !ok {
		return fmt.Errorf("invalid chain ID: %q", chainID)
	}
	return nil
}

// ReloadConfig re-reads config.yaml and replaces Config + EnabledChains in memory.
func ReloadConfig() error {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		return fmt.Errorf("ReloadConfig: %w", err)
	}
	var newCfg config
	if err := yaml.Unmarshal(data, &newCfg); err != nil {
		return fmt.Errorf("ReloadConfig: %w", err)
	}
	ConfigMu.Lock()
	defer ConfigMu.Unlock()
	Config = newCfg
	EnabledChains = nil
	for id, chain := range Config.Chains {
		if chain.Enabled {
			EnabledChains = append(EnabledChains, id)
		}
	}
	sort.Strings(EnabledChains)
	Config.AllowedOrigins = nil
	for _, raw := range strings.Split(Config.AllowOrigin, ",") {
		if origin := strings.TrimSpace(raw); origin != "" {
			Config.AllowedOrigins = append(Config.AllowedOrigins, origin)
		}
	}
	log.Printf("[config] reloaded: enabled chains=%v", EnabledChains)
	return nil
}

// WriteConfig serializes the current in-memory Config to config.yaml.
// Note: the written file will not preserve comments from the original template.
func WriteConfig() error {
	ConfigMu.Lock()
	data, err := yaml.Marshal(Config)
	ConfigMu.Unlock()
	if err != nil {
		return fmt.Errorf("WriteConfig: %w", err)
	}
	return os.WriteFile("config.yaml", data, 0644)
}

// AddChain adds a new chain to Config and, if enabled, to EnabledChains.
// Returns an error if the chain ID already exists.
func AddChain(id string, cfg *ChainConfig) error {
	ConfigMu.Lock()
	defer ConfigMu.Unlock()
	if _, exists := Config.Chains[id]; exists {
		return fmt.Errorf("chain %q already exists", id)
	}
	if Config.Chains == nil {
		Config.Chains = make(map[string]*ChainConfig)
	}
	Config.Chains[id] = cfg
	if cfg.Enabled {
		EnabledChains = append(EnabledChains, id)
		sort.Strings(EnabledChains)
	}
	return nil
}

// RemoveChain removes a chain from Config and EnabledChains.
func RemoveChain(id string) {
	ConfigMu.Lock()
	defer ConfigMu.Unlock()
	delete(Config.Chains, id)
	for i, c := range EnabledChains {
		if c == id {
			EnabledChains = append(EnabledChains[:i], EnabledChains[i+1:]...)
			break
		}
	}
}

// SetChainEnabled updates the Enabled flag for a chain and rebuilds EnabledChains.
func SetChainEnabled(id string, enabled bool) error {
	ConfigMu.Lock()
	defer ConfigMu.Unlock()
	chain, ok := Config.Chains[id]
	if !ok {
		return fmt.Errorf("unknown chain %q", id)
	}
	chain.Enabled = enabled
	EnabledChains = nil
	for cid, c := range Config.Chains {
		if c.Enabled {
			EnabledChains = append(EnabledChains, cid)
		}
	}
	sort.Strings(EnabledChains)
	return nil
}

// GetEnabledChainIDs returns a sorted slice of all enabled chain IDs.
func (c *config) GetEnabledChainIDs() []string {
	var ids []string
	for id, chain := range c.Chains {
		if chain.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
func SendDiscordAlert(msg string, webhookURL string) error {
	payload := map[string]string{"content": msg}
	body, _ := json.Marshal(payload)

	resp, err := alertHTTPClient.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error sending Discord alert: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook HTTP status: %d", resp.StatusCode)
	}
	return nil
}

func SendSlackAlert(msg string, webhookURL string) error {
	payload := map[string]string{"text": msg}
	body, _ := json.Marshal(payload)

	resp, err := alertHTTPClient.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Erreur sending Slack alert : %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Slack webhook HTTP %d", resp.StatusCode)
	}
	return nil
}
func SendAllValidatorAlerts(chainID string, missed int, today, level, addr, moniker string, start_height, end_height int64, db *gorm.DB) error {
	type Webhook struct {
		UserID  string
		URL     string
		Type    string
		ID      int
		ChainID *string
	}
	var fullMsg string
	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).
		Where("chain_id = ? OR chain_id IS NULL", chainID).
		Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}

	chainLabel := fmt.Sprintf("[%s] ", chainID)

	for _, wh := range webhooks {

		// ================== Build msg ===============

		var emoji, prefix string
		switch wh.Type {
		case "discord":
			if level == "CRITICAL" {
				emoji = "🚨"
				prefix = "**"

				type tag struct{ MentionTag string }
				var res []tag
				err := db.Model(&database.AlertContact{}).
					Select("mention_tag").
					Where("user_id = ? AND moniker = ? AND id_webhook = ?", wh.UserID, moniker, wh.ID).
					Find(&res).Error

				fullMsg = fmt.Sprintf(
					"%s%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
					chainLabel, emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
				)
				if err != nil {
					return fmt.Errorf("failed to fetch mentions: %w", err)
				}
				for _, r := range res {
					fullMsg += "\n<@" + r.MentionTag + ">"
				}
			}
			if level == "WARNING" {
				emoji = "⚠️"
				prefix = ""
				fullMsg = fmt.Sprintf(
					"%s%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
					chainLabel, emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
				)

			}
			log.Println(fullMsg)
			sendErr := SendDiscordAlert(fullMsg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		case "slack":
			if level == "CRITICAL" {
				emoji = "🚨"
				prefix = "*"

				type tag struct{ MentionTag string }
				log.Printf("TYPE SLACK")
				var res []tag
				err := db.Model(&database.AlertContact{}).
					Select("mention_tag").
					Where("user_id = ? AND moniker = ? AND id_webhook = ?", wh.UserID, moniker, wh.ID).
					Find(&res).Error
				if err != nil {
					return fmt.Errorf("failed to fetch mentions: %w", err)
				}
				fullMsg = fmt.Sprintf(
					"%s%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
					chainLabel, emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
				)
				for _, r := range res {
					fullMsg += "\n <@" + r.MentionTag + ">"
				}
				if level == "WARNING" {
					emoji = "⚠️"
					prefix = ""
					fullMsg = fmt.Sprintf(
						"%s%s %s%s %s %s\naddr: %s\nmoniker: %s\nmissed %d blocks (%d -> %d)",
						chainLabel, emoji, prefix, level, prefix, today, addr, moniker, missed, start_height, end_height,
					)
				}
			}
			sendErr := SendSlackAlert(fullMsg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		default:
			continue
		}

	}
	// ======================================== TELEGRAM
	if level == "CRITICAL" {
		emoji := "🚨"
		fullMsg = fmt.Sprintf(
			"%s%s <b>%s</b> %s\n"+
				"addr: <code>%s</code>\n"+
				"moniker: <b>%s</b>\n"+
				"missed %d blocks (%d → %d)",
			html.EscapeString(chainLabel),
			emoji,
			html.EscapeString(level),
			html.EscapeString(today),
			html.EscapeString(addr),
			html.EscapeString(moniker),
			missed, start_height, end_height,
		)
	}

	if level == "WARNING" {
		emoji := "⚠️"
		fullMsg = fmt.Sprintf(
			"%s%s <b>%s</b> %s\n"+
				"addr: <code>%s</code>\n"+
				"moniker: <b>%s</b>\n"+
				"missed %d blocks (%d → %d)",
			html.EscapeString(chainLabel),
			emoji,
			html.EscapeString(level),
			html.EscapeString(today),
			html.EscapeString(addr),
			html.EscapeString(moniker),
			missed, start_height, end_height,
		)
	}

	if err := telegram.MsgTelegramAlert(fullMsg, addr, chainID, Config.TokenTelegramValidator, "validator", db); err != nil {
		log.Printf("❌ MsgTelegramAlert: %v", err)
	}

	return nil
}

func SendUserReportAlert(userID, chainID, msg string, db *gorm.DB) error {
	type Webhook struct {
		URL  string
		Type string
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).
		Select("url", "type").
		Where("user_id = ? AND chain_id = ?", userID, chainID).
		Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks for user %s: %w", userID, err)
	}

	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			if err := SendDiscordAlert(msg, wh.URL); err != nil {
				return fmt.Errorf("failed to send Discord alert: %w", err)
			}
		case "slack":
			if err := SendSlackAlert(msg, wh.URL); err != nil {
				return fmt.Errorf("failed to send Slack alert: %w", err)
			}
		default:
			log.Printf("⚠️ Unknown webhook type for user %s: %s", userID, wh.Type)
		}
	}

	return nil
}
func SendResolveValidator(chainID, msg string, addr string, db *gorm.DB) error {
	type Webhook struct {
		UserID  string
		URL     string
		Type    string
		ID      int
		ChainID *string
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).
		Where("chain_id = ? OR chain_id IS NULL", chainID).
		Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			sendErr := SendDiscordAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		case "slack":
			sendErr := SendSlackAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		}
	}
	if err := telegram.MsgTelegramAlert(msg, addr, chainID, Config.TokenTelegramValidator, "validator", db); err != nil {
		log.Printf("❌ MsgTelegramAlert: %v", err)
	}

	return nil
}

func SendInfoValidator(chainID, msg string, level string, db *gorm.DB) error {
	type Webhook struct {
		UserID  string
		URL     string
		Type    string
		ID      int
		ChainID *string
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).
		Where("chain_id = ? OR chain_id IS NULL", chainID).
		Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			sendErr := SendDiscordAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		case "slack":
			sendErr := SendSlackAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		}
	}
	if err := telegram.MsgTelegram(msg, Config.TokenTelegramValidator, "validator", db); err != nil {
		log.Printf("❌ MsgTelegram: %v", err)
	}
	return nil
}

func MultiSendReportGovdao(chainID string, id int, title, urlgnoweb, urltx string, db *gorm.DB) error {

	type Webhook struct {
		UserID        string
		URL           string
		Type          string
		LastCheckedID int
	}
	var webhooks []Webhook
	if err := db.Model(&database.WebhookGovDAO{}).Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {
		if err := SendReportGovdao(chainID, id, title, urlgnoweb, urltx, wh.Type, wh.URL); err != nil {
			log.Printf("❌ SendReportGovdao: %v", err)
		}
	}
	// build msg for telegram and send to all chatids
	msg := telegram.FormatTelegramMsg(chainID, id, title, urlgnoweb, urltx)
	err := telegram.MsgTelegram(msg, Config.TokenTelegramGovdao, "govdao", db)
	if err != nil {
		log.Printf("error send govdao telegram  %s", err)
	}

	return nil

}

func SendReportGovdao(chainID string, id int, title, urlgnoweb, urltx, typew string, urlwebhook string) error {

	switch typew {
	case "discord":

		msg := fmt.Sprintf("--- \n"+
			"🗳️ ** [%s] New Proposal N° %d: %s ** -  \n"+
			"🔗source: %s \n"+
			"🗒️Tx: %s \n"+
			"🖐️ Interact & Vote: https://memba.samourai.app/dao/gno.land~r~gov~dao/proposal/%d \n"+
			" ** Make sure you're using the appropriate network on Memba **",
			chainID, id, title, urlgnoweb, urltx, id)
		log.Println(msg)
		sendErr := SendDiscordAlert(msg, urlwebhook)
		if sendErr != nil {
			log.Printf("❌ Failed to send alert to %s (%s): %v", urlwebhook, typew, sendErr)

		}

	case "slack":
		msg := fmt.Sprintf("--- \n"+
			"🗳️ * [%s] New Proposal N° %d: %s * -  \n"+
			"🔗source: %s \n"+
			"🗒️Tx: %s \n"+
			"🖐️ Interact & Vote: https://memba.samourai.app/dao/gno.land~r~gov~dao/proposal/%d \n"+
			"* Make sure you're using the appropriate network on Memba *",
			chainID, id, title, urlgnoweb, urltx, id)

		sendErr := SendSlackAlert(msg, urlwebhook)
		if sendErr != nil {
			log.Printf("❌ Failed to send alert to %s (%s): %v", urlwebhook, typew, sendErr)

		}

	}

	return nil

}

func SendInfoGovdao(chainID string, msg string, db *gorm.DB) error {
	msg = fmt.Sprintf("[%s] %s", chainID, msg)
	type Webhook struct {
		UserID        string
		URL           string
		Type          string
		LastCheckedID int
	}

	var webhooks []Webhook
	if err := db.Model(&database.WebhookGovDAO{}).Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}
	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			sendErr := SendDiscordAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		case "slack":
			sendErr := SendSlackAlert(msg, wh.URL)
			if sendErr != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, sendErr)
				continue
			}

		}
		// database.InsertAlertlog(db, wh.addr, "moniker", level, wh.URL, 0, 0, true, msg, time.Now())

	}
	return nil
}
