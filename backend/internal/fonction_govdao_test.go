package internal

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendInfoGovdao_FiltersWebhooksByChain pins that a GovDAO notice about one
// chain reaches the webhooks of that chain and the chain-less ones only.
func TestSendInfoGovdao_FiltersWebhooksByChain(t *testing.T) {
	db := testoutils.NewTestDB(t)

	var mu sync.Mutex
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	orig := alertHTTPClient
	alertHTTPClient = &http.Client{Timeout: 10 * time.Second}
	defer func() { alertHTTPClient = orig }()

	chainA, chainB := "chainA", "chainB"
	for _, wh := range []database.WebhookGovDAO{
		{UserID: "a", URL: srv.URL + "/a", Type: "discord", ChainID: &chainA},
		{UserID: "b", URL: srv.URL + "/b", Type: "discord", ChainID: &chainB},
		{UserID: "g", URL: srv.URL + "/global", Type: "discord"},
	} {
		require.NoError(t, db.Create(&wh).Error)
	}

	require.NoError(t, SendInfoGovdao(chainA, "halt scheduled", db))
	sort.Strings(hits)
	assert.Equal(t, []string{"/a", "/global"}, hits)
}
