package gnovalidator

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetWithFailover_UsesPrimaryWhenHealthy(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/validators", r.URL.Path)
		_, _ = w.Write([]byte(`{"result":{"validators":[]}}`))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("backup must not be called while the primary answers")
	}))
	defer backup.Close()

	resp, used, err := getWithFailover([]string{primary.URL, backup.URL}, "/validators")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, primary.URL, used)
	body, _ := io.ReadAll(resp.Body)
	assert.JSONEq(t, `{"result":{"validators":[]}}`, string(body))
}

func TestGetWithFailover_FallsBackWhenPrimaryIs5xx(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backup.Close()

	resp, used, err := getWithFailover([]string{primary.URL, backup.URL}, "/validators")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, backup.URL, used)
}

func TestGetWithFailover_FallsBackWhenPrimaryIsUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // nothing listens on deadURL any more

	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer backup.Close()

	resp, used, err := getWithFailover([]string{deadURL, backup.URL}, "/validators")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, backup.URL, used)
}

func TestGetWithFailover_AllEndpointsFail(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer s.Close()

	_, _, err := getWithFailover([]string{s.URL, s.URL}, "/validators")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "all 2")
}

func TestGetWithFailover_TrimsTrailingSlash(t *testing.T) {
	var gotPath string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
	}))
	defer s.Close()

	resp, _, err := getWithFailover([]string{s.URL + "/"}, "/validators")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "/validators", gotPath)
}

func TestRPCHTTPClient_HasATimeout(t *testing.T) {
	// http.DefaultClient has none; a hung endpoint would otherwise block
	// InitMonikerMap, and therefore the whole monitoring startup, forever.
	assert.Positive(t, rpcHTTPClient.Timeout)
}
