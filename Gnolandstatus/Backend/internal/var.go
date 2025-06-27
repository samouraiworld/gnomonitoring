package internal

import (
	"flag"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

var MonikerMutex sync.RWMutex

// Config Yaml
type config struct {
	RPCEndpoint       string `yaml:"rpc_endpoint"`
	DiscordWebhookURL string `yaml:"discord_webhook_url"`
	WindowSize        int    `yaml:"windows_size"`
	DailyReportHour   int    `yaml:"daily_report_hour"`
	DailyReportMinute int    `yaml:"daily_report_minute"`
	MetricsPort       int    `yaml:"metrics_port"`
}

var TestAlert = flag.Bool("test-alert", false, "Send alert test for Discord")

var Config config

type BlockParticipation struct {
	Height     int64
	Validators map[string]bool
}

var (
	BlockWindow []BlockParticipation
	//windowSize        = 100
	ParticipationRate = make(map[string]float64)
	LastAlertSent     = make(map[string]int64) // to avoid spamming
	MonikerMap        = make(map[string]string)
)

// prometheus var
var ValidatorParticipation = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "gnoland_validator_participation_rate",
		Help: "Validator participation rate (%) over the sliding window",
	},
	[]string{"validator_address", "moniker"},
)

// prometheus var start and end block analyse
var (
	BlockWindowStartHeight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gnoland_block_window_start_height",
			Help: "Start height of the current block window",
		},
	)
	BlockWindowEndHeight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "gnoland_block_window_end_height",
			Help: "End height of the current block window",
		},
	)
)
