package score

import "testing"

func TestWeightsFromConfigDefaults(t *testing.T) {
	w := WeightsFromConfig(map[string]string{})
	if w != DefaultWeights() {
		t.Fatalf("empty config should yield defaults, got %+v", w)
	}
}

func TestWeightsFromConfigOverrides(t *testing.T) {
	w := WeightsFromConfig(map[string]string{
		"report_score_critical_weight":           "10",
		"report_score_critical_cap":              "50",
		"report_score_downtime_blocks_per_point": "1000",
		"report_score_downtime_cap":              "15",
	})
	want := Weights{CriticalWeight: 10, CriticalCap: 50, DowntimeBlocksPerPoint: 1000, DowntimeCap: 15}
	if w != want {
		t.Fatalf("got %+v, want %+v", w, want)
	}
}

func TestWeightsFromConfigIgnoresInvalid(t *testing.T) {
	w := WeightsFromConfig(map[string]string{"report_score_critical_weight": "abc"})
	if w.CriticalWeight != DefaultWeights().CriticalWeight {
		t.Fatalf("invalid value should fall back to default, got %d", w.CriticalWeight)
	}
}
