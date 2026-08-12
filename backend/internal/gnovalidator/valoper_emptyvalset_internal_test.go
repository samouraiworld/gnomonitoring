package gnovalidator

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gnolang/gno/gno.land/pkg/gnoclient"
	"github.com/samouraiworld/gnomonitoring/backend/internal"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitMonikerMap_EmptyValsetKeepsPreviousMap(t *testing.T) {
	db := testoutils.NewTestDB(t)
	const chainID = "test-empty-valset"

	// Seed a known-good map, as a healthy refresh cycle would have left it.
	ReplaceMonikerMap(chainID, map[string]string{"g1aaa": "alice", "g1bbb": "bob"})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/validators":
			_, _ = w.Write([]byte(`{"result":{"block_height":"100","validators":[]}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := &internal.ChainConfig{RPCEndpoints: []string{srv.URL}}
	got := InitMonikerMap(db, chainID, gnoclient.Client{}, cfg)

	require.Nil(t, got)
	assert.Equal(t, map[string]string{"g1aaa": "alice", "g1bbb": "bob"}, GetMonikerMap(chainID),
		"an empty /validators response must not wipe the valset")
}
