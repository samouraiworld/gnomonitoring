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
	// Non-overridden fields must retain their DefaultWeights() values.
	want := DefaultWeights()
	want.CriticalWeight = 10
	want.CriticalCap = 50
	want.DowntimeBlocksPerPoint = 1000
	want.DowntimeCap = 15
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

func TestWeightsFromConfig_NewKeys(t *testing.T) {
	cfg := map[string]string{
		KeyWarningWeight:       "3",
		KeyWarningCap:          "15",
		KeySignWeight:          "0.7",
		KeyProposerWeight:      "0.3",
		KeyProposerMinExpected: "10",
		KeyVpSeverityFactor:    "0.25",
	}
	w := WeightsFromConfig(cfg)
	if w.WarningWeight != 3 || w.WarningCap != 15 || w.ProposerMinExpected != 10 {
		t.Fatalf("int keys not parsed: %+v", w)
	}
	if w.SignWeight != 0.7 || w.ProposerWeight != 0.3 || w.VpSeverityFactor != 0.25 {
		t.Fatalf("float keys not parsed: %+v", w)
	}
}

func TestWeightsFromConfig_DefaultsWhenMissing(t *testing.T) {
	w := WeightsFromConfig(map[string]string{})
	d := DefaultWeights()
	if w != d {
		t.Fatalf("empty config should equal DefaultWeights: got %+v want %+v", w, d)
	}
}

func TestWeightsFromConfig_FreqKeys(t *testing.T) {
	w := WeightsFromConfig(map[string]string{
		KeyFreqWeight: "0.5",
		KeyFreqCap:    "40",
	})
	if w.FreqWeight != 0.5 || w.FreqCap != 40 {
		t.Fatalf("freq keys not parsed: %+v", w)
	}
}

func TestWeightsFromConfig_FreqDefaultsWhenMissing(t *testing.T) {
	w := WeightsFromConfig(map[string]string{})
	if w.FreqWeight != 3.0/7.0 || w.FreqCap != 30 {
		t.Fatalf("want default FreqWeight=3/7 FreqCap=30, got %+v", w)
	}
}

func TestWeightsFromConfig_FreqWeightAcceptsDecimal(t *testing.T) {
	w := WeightsFromConfig(map[string]string{KeyFreqWeight: "0.6"})
	if w.FreqWeight != 0.6 {
		t.Fatalf("FreqWeight = %v, want 0.6", w.FreqWeight)
	}
}
