package internal

import "flag"

var (
	DisableReport *bool
	EnableDebug   *bool
)

func InitFlags() {
	DisableReport = flag.Bool("disable-report", false, "Disable the daily report scheduler")

	flag.Parse()
}
