package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
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

type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"` // "disable" for local docker-compose, "require" for managed PG
}

func (c DatabaseConfig) DSN() string {
	sslmode := c.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Password, c.DBName, sslmode)
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
	Database               DatabaseConfig          `yaml:"database"`

	// Parsed at load time from AllowOrigin (comma-separated).
	AllowedOrigins []string `yaml:"-"`
}

var Config config

// EnabledChains holds the IDs of all chains with Enabled: true, sorted alphabetically.
var EnabledChains []string

// isPublicUnicastIP reports whether ip is safe to connect to for an
// outbound webhook: not loopback, not link-local (this also blocks the
// 169.254.169.254 cloud metadata endpoint), not a private (RFC1918/RFC4193)
// range, not unspecified, and not multicast.
func isPublicUnicastIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return true
}

// guardedDialContext resolves the target address and refuses to connect if
// any resolved IP is not a public unicast address. This closes the
// DNS-rebinding gap left by registration-time-only URL validation
// (validateWebhookURL in internal/api/api.go): the allowlisted hostname
// (discord.com, hooks.slack.com, ...) could in principle later resolve to an
// internal address, and this check re-validates on every connection attempt,
// not just once at webhook-registration time.
func guardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses found for %s", host)
	}
	for _, ip := range ips {
		if !isPublicUnicastIP(ip) {
			return nil, fmt.Errorf("refusing to dial non-public address %s (resolved from %s)", ip, host)
		}
	}
	// Try every validated address in order rather than only ips[0]: a
	// transient issue reaching the first resolved address (e.g. an IPv6
	// address during a partial network/IPv6 outage) shouldn't fail the whole
	// dial when another already-validated address would have worked.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var lastErr error
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// alertHTTPClient is reused across all webhook dispatches to enable TCP
// connection pooling. Its transport re-validates the resolved IP on every
// connection (see guardedDialContext) so a webhook host that later resolves
// to a private/loopback address (DNS rebinding) is refused at send time, not
// just at registration time.
var alertHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: guardedDialContext,
	},
}

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
// DiscordEmbed mirrors the subset of Discord's embed object this project
// uses. See https://discord.com/developers/docs/resources/channel#embed-object
// for the full schema.
type DiscordEmbed struct {
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color,omitempty"`
	Fields      []DiscordEmbedField `json:"fields,omitempty"`
	Footer      *DiscordEmbedFooter `json:"footer,omitempty"`
}

type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type DiscordEmbedFooter struct {
	Text string `json:"text"`
}

// SendDiscordEmbed posts a single rich embed to a Discord webhook, used by
// the daily report for a channel-appropriate rendering instead of a plain
// text message (see SendDiscordAlert for the plain-text incident-alert path,
// unchanged).
func SendDiscordEmbed(embed DiscordEmbed, webhookURL string) error {
	payload := map[string]any{"embeds": []DiscordEmbed{embed}}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord embed: %w", err)
	}

	resp, err := alertHTTPClient.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error sending Discord embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook HTTP status: %d", resp.StatusCode)
	}
	return nil
}

// SendDiscordAlertEmbed posts a single rich embed alongside a plain-text
// content string to a Discord webhook. content carries @mentions, which
// Discord does not parse when written inside an embed (see
// RenderAlertDiscordEmbed); an empty content is omitted from the payload
// entirely rather than sent as "".
func SendDiscordAlertEmbed(content string, embed DiscordEmbed, webhookURL string) error {
	payload := map[string]any{"embeds": []DiscordEmbed{embed}}
	if content != "" {
		payload["content"] = content
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord alert embed: %w", err)
	}

	resp, err := alertHTTPClient.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error sending Discord alert embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord webhook HTTP status: %d", resp.StatusCode)
	}
	return nil
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

// SlackBlock mirrors the subset of Slack's Block Kit this project uses. See
// https://api.slack.com/block-kit for the full schema.
type SlackBlock struct {
	Type     string      `json:"type"`
	Text     *SlackText  `json:"text,omitempty"`
	Elements []SlackText `json:"elements,omitempty"`
}

type SlackText struct {
	Type string `json:"type"` // "mrkdwn" or "plain_text"
	Text string `json:"text"`
}

// SendSlackBlocks posts a Block Kit message to a Slack incoming webhook,
// used by the daily report for a channel-appropriate rendering instead of a
// plain text message (see SendSlackAlert for the plain-text incident-alert
// path, unchanged). Unlike SendSlackAlert, this returns a non-nil error on a
// non-2xx response rather than only logging it.
func SendSlackBlocks(blocks []SlackBlock, webhookURL string) error {
	payload := map[string]any{"blocks": blocks}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal slack blocks: %w", err)
	}

	resp, err := alertHTTPClient.Post(webhookURL, "application/json", bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("error sending Slack blocks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook HTTP status: %d", resp.StatusCode)
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
	var webhooks []Webhook
	if err := db.Model(&database.WebhookValidator{}).
		Where("chain_id = ? OR chain_id IS NULL", chainID).
		Find(&webhooks).Error; err != nil {
		return fmt.Errorf("failed to fetch webhooks: %w", err)
	}

	var alertLevel AlertLevel
	var emoji string
	switch level {
	case "CRITICAL":
		alertLevel = AlertCritical
		emoji = "🚨"
	case "WARNING":
		alertLevel = AlertWarning
		emoji = "⚠️"
	}

	data := AlertData{
		ChainID: chainID,
		Level:   alertLevel,
		Emoji:   emoji,
		Title:   level,
		Date:    today,
		Fields: []AlertField{
			{Name: "addr", Value: addr},
			{Name: "moniker", Value: moniker},
			{Name: "missed blocks", Value: fmt.Sprintf("%d (%d -> %d)", missed, start_height, end_height)},
		},
	}

	for _, wh := range webhooks {
		whData := data
		if level == "CRITICAL" && (wh.Type == "discord" || wh.Type == "slack") {
			type tag struct{ MentionTag string }
			var res []tag
			if err := db.Model(&database.AlertContact{}).
				Select("mention_tag").
				Where("user_id = ? AND moniker = ? AND id_webhook = ?", wh.UserID, moniker, wh.ID).
				Find(&res).Error; err != nil {
				return fmt.Errorf("failed to fetch mentions: %w", err)
			}
			for _, r := range res {
				whData.Mentions = append(whData.Mentions, r.MentionTag)
			}
		}

		switch wh.Type {
		case "discord":
			content, embed := RenderAlertDiscordEmbed(whData)
			if err := SendDiscordAlertEmbed(content, embed, wh.URL); err != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, err)
				continue
			}
		case "slack":
			blocks := RenderAlertSlackBlocks(whData)
			if err := SendSlackBlocks(blocks, wh.URL); err != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, err)
				continue
			}
		default:
			continue
		}
	}

	text := RenderAlertTelegramHTML(data)
	if err := telegram.MsgTelegramAlert(text, addr, chainID, Config.TokenTelegramValidator, "validator", db); err != nil {
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
func SendResolveValidator(chainID, addr, moniker string, resumeHeight int64, db *gorm.DB) error {
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

	data := AlertData{
		ChainID: chainID,
		Level:   AlertResolved,
		Emoji:   "✅",
		Title:   "RESOLVED",
		Fields: []AlertField{
			{Name: "validator", Value: fmt.Sprintf("%s (%s)", moniker, addr)},
			{Name: "resolved at block", Value: fmt.Sprintf("%d", resumeHeight)},
		},
	}

	for _, wh := range webhooks {
		switch wh.Type {
		case "discord":
			content, embed := RenderAlertDiscordEmbed(data)
			if err := SendDiscordAlertEmbed(content, embed, wh.URL); err != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, err)
				continue
			}
		case "slack":
			blocks := RenderAlertSlackBlocks(data)
			if err := SendSlackBlocks(blocks, wh.URL); err != nil {
				log.Printf("❌ Failed to send alert to %s (%s): %v", wh.URL, wh.Type, err)
				continue
			}
		}
	}
	text := RenderAlertTelegramHTML(data)
	if err := telegram.MsgTelegramAlert(text, addr, chainID, Config.TokenTelegramValidator, "validator", db); err != nil {
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
	if err := telegram.MsgTelegramChain(msg, chainID, Config.TokenTelegramValidator, "validator", db); err != nil {
		log.Printf("❌ MsgTelegramChain: %v", err)
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
