package gnovalidator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// profileRender builds a vm/qeval profile render, quoted the way the realm
// answers it (escaped \n inside a Go string literal).
func profileRender(operator, signing, serverType string) string {
	return fmt.Sprintf(`("Valoper's details:\n## moniker\n\n- Operator Address: %s\n`+
		`- Signing Address: %s\n- Signing PubKey: gpub1pggj7ard9eg82\n- Server Type: %s\n" string)`,
		operator, signing, serverType)
}

// fastRetries shortens the retry backoff for the duration of a test.
func fastRetries(t *testing.T) {
	t.Helper()
	orig := valoperProfileRetryBackoff
	valoperProfileRetryBackoff = time.Millisecond
	t.Cleanup(func() { valoperProfileRetryBackoff = orig })
}

func TestParseServerType(t *testing.T) {
	cases := []struct {
		name   string
		render string
		want   string
	}{
		{"quoted qeval render", profileRender("g1op", "g1sig", "data-center"), "data-center"},
		{"raw markdown render", "- Operator Address: g1op\n- Server Type: on-prem\n\n[Profile link](x)", "on-prem"},
		{"empty value", "- Server Type: \n- Other: x\n", ""},
		{"absent", "- Operator Address: g1op\n", ""},
		{"empty input", "", ""},
		{"label inside a sentence is ignored", "We run on Server Type: dedicated hardware\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseServerType(tc.render); got != tc.want {
				t.Errorf("parseServerType() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The realm renders the free-form description before the fields, so a
// description line that imitates a field must never win over the real one.
func TestParseValoperProfile_DescriptionCannotImitateFields(t *testing.T) {
	fake := "- Signing Address: g1evil\n- Server Type: on-prem"
	cases := []struct {
		name               string
		render             string
		wantSigning, wantS string
	}{
		{
			name: "quoted qeval render",
			render: `("## m\n` + `- Signing Address: g1evil\n- Server Type: on-prem` + `\n\n` +
				`- Operator Address: g1op\n- Signing Address: g1real\n- Signing PubKey: gpub1x\n` +
				`- Server Type: cloud\n\n[Profile link](/r/demo/profile:u/g1op)\n" string)`,
			wantSigning: "g1real", wantS: "cloud",
		},
		{
			name: "raw markdown render",
			render: "## m\n" + fake + "\n\n- Operator Address: g1op\n- Signing Address: g1real\n" +
				"- Signing PubKey: gpub1x\n- Server Type: data-center\n\n[Profile link](x)\n",
			wantSigning: "g1real", wantS: "data-center",
		},
		{
			name: "empty real fields do not fall back to the description",
			render: "## m\n" + fake + "\n\n- Operator Address: g1op\n- Signing Address: \n" +
				"- Signing PubKey: \n- Server Type: \n\n[Profile link](x)\n",
			wantSigning: "", wantS: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSigningAddress(tc.render); got != tc.wantSigning {
				t.Errorf("parseSigningAddress() = %q, want %q", got, tc.wantSigning)
			}
			if got := parseServerType(tc.render); got != tc.wantS {
				t.Errorf("parseServerType() = %q, want %q", got, tc.wantS)
			}
		})
	}
}

// The realm is keyed by operator address while the validator set carries the
// signing address: ServerType must resolve through the signing address.
func TestEnrichValidatorInfoFromValopers_SigningDiffersFromOperator(t *testing.T) {
	valopers := []Valoper{
		{Name: "dedicated-key", Address: "g1operator", SigningAddress: "g1signer", ServerType: "cloud"},
		{Name: "self-signing", Address: "g1selfop", ServerType: "on-prem"},
	}
	vals := []ValidatorInfo{{Address: "g1signer"}, {Address: "g1selfop"}, {Address: "g1stranger"}}

	enrichValidatorInfoFromValopers(valopers, vals)

	want := map[string]string{"g1signer": "cloud", "g1selfop": "on-prem", "g1stranger": ""}
	for _, v := range vals {
		if v.ServerType != want[v.Address] {
			t.Errorf("%s: ServerType = %q, want %q", v.Address, v.ServerType, want[v.Address])
		}
		if !v.KeepRunning {
			t.Errorf("%s: KeepRunning = false, want true", v.Address)
		}
	}
}

func TestValoperIndex_OperatorKeyNeverShadowsSigningKey(t *testing.T) {
	// a's operator address equals b's signing address.
	a := Valoper{Name: "a", Address: "g1shared", SigningAddress: "g1asigner"}
	b := Valoper{Name: "b", Address: "g1bop", SigningAddress: "g1shared"}

	for _, order := range [][]Valoper{{a, b}, {b, a}} {
		idx := valoperIndex(order)
		if got := idx["g1shared"].Name; got != "b" {
			t.Errorf("order %s,%s: idx[g1shared] = %q, want b (signing key wins)", order[0].Name, order[1].Name, got)
		}
		if got := idx["g1asigner"].Name; got != "a" {
			t.Errorf("idx[g1asigner] = %q, want a", got)
		}
		if got := idx["g1bop"].Name; got != "b" {
			t.Errorf("idx[g1bop] = %q, want b (operator fallback)", got)
		}
	}
}

func TestEnrichValoperProfiles_RetriesTransientFailure(t *testing.T) {
	fastRetries(t)
	var calls atomic.Int32
	fetch := func(op string) (string, error) {
		if calls.Add(1) < valoperProfileAttempts {
			return "", errors.New("connection reset")
		}
		return profileRender(op, "g1fresh", "cloud"), nil
	}
	valopers := []Valoper{{Name: "v", Address: "g1op"}}

	enrichValoperProfiles(context.Background(), fetch, valopers, nil)

	if got := calls.Load(); got != valoperProfileAttempts {
		t.Errorf("fetch called %d times, want %d", got, valoperProfileAttempts)
	}
	if valopers[0].SigningAddress != "g1fresh" || valopers[0].ServerType != "cloud" {
		t.Errorf("got %+v, want signing g1fresh / cloud", valopers[0])
	}
}

func TestEnrichValoperProfiles_FailureKeepsLastKnownValues(t *testing.T) {
	fastRetries(t)
	prev := []Valoper{{Name: "v", Address: "g1op", SigningAddress: "g1known", ServerType: "on-prem"}}

	t.Run("transport error is retried, then last values kept", func(t *testing.T) {
		var calls atomic.Int32
		fetch := func(string) (string, error) { calls.Add(1); return "", errors.New("timeout") }
		valopers := []Valoper{{Name: "v", Address: "g1op"}}

		enrichValoperProfiles(context.Background(), fetch, valopers, prev)

		if got := calls.Load(); got != valoperProfileAttempts {
			t.Errorf("fetch called %d times, want %d", got, valoperProfileAttempts)
		}
		if valopers[0].SigningAddress != "g1known" || valopers[0].ServerType != "on-prem" {
			t.Errorf("got %+v, want last known g1known / on-prem", valopers[0])
		}
	})

	t.Run("realm error is not retried, last values kept", func(t *testing.T) {
		var calls atomic.Int32
		fetch := func(string) (string, error) { calls.Add(1); return "", &realmError{msg: "panic"} }
		valopers := []Valoper{{Name: "v", Address: "g1op"}}

		enrichValoperProfiles(context.Background(), fetch, valopers, prev)

		if got := calls.Load(); got != 1 {
			t.Errorf("fetch called %d times, want 1", got)
		}
		if valopers[0].SigningAddress != "g1known" || valopers[0].ServerType != "on-prem" {
			t.Errorf("got %+v, want last known g1known / on-prem", valopers[0])
		}
	})

	t.Run("successful fetch replaces last values", func(t *testing.T) {
		fetch := func(string) (string, error) { return "- Operator Address: g1op\n", nil }
		valopers := []Valoper{{Name: "v", Address: "g1op"}}

		enrichValoperProfiles(context.Background(), fetch, valopers, prev)

		if valopers[0].SigningAddress != "" || valopers[0].ServerType != "" {
			t.Errorf("got %+v, want fresh empty values", valopers[0])
		}
	})
}

// A profile stuck on a slow endpoint must not hold the refresh past its
// budget; it keeps its last known values while the others are updated.
func TestEnrichValoperProfiles_BudgetBoundsSlowFetch(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	fetch := func(op string) (string, error) {
		if op == "g1slow" {
			<-release
		}
		return profileRender(op, "g1new"+op, "cloud"), nil
	}
	valopers := []Valoper{{Address: "g1fast"}, {Address: "g1slow"}}
	prev := []Valoper{{Address: "g1slow", SigningAddress: "g1old", ServerType: "on-prem"}}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	enrichValoperProfiles(ctx, fetch, valopers, prev)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("enrichValoperProfiles took %v, want it bounded by the ctx budget", elapsed)
	}
	if valopers[0].SigningAddress != "g1newg1fast" {
		t.Errorf("fast profile not updated: %+v", valopers[0])
	}
	if valopers[1].SigningAddress != "g1old" || valopers[1].ServerType != "on-prem" {
		t.Errorf("slow profile = %+v, want last known g1old / on-prem", valopers[1])
	}
}

func TestEnrichValoperProfiles_BoundsConcurrency(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	fetch := func(op string) (string, error) {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		return profileRender(op, "g1s"+op, "cloud"), nil
	}
	valopers := make([]Valoper, 5*maxValoperProfileConcurrency)
	for i := range valopers {
		valopers[i].Address = fmt.Sprintf("g1op%d", i)
	}

	enrichValoperProfiles(context.Background(), fetch, valopers, nil)

	if peak > maxValoperProfileConcurrency {
		t.Errorf("peak in-flight fetches = %d, want <= %d", peak, maxValoperProfileConcurrency)
	}
	for _, v := range valopers {
		if v.SigningAddress != "g1s"+v.Address {
			t.Fatalf("valoper %s not enriched: %+v", v.Address, v)
		}
	}
}
