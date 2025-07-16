package gnovalidator

import (
	"github.com/prometheus/client_golang/prometheus"
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

func Init() {
	prometheus.MustRegister(ValidatorParticipation)
	prometheus.MustRegister(BlockWindowStartHeight)
	prometheus.MustRegister(BlockWindowEndHeight)
}
