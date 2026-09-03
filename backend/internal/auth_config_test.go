package internal

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadConfigFrom writes body to a config.yaml in a temp dir, runs LoadConfig
// there, and restores the working directory afterwards. It mirrors the setup
// the other config tests do by hand.
func loadConfigFrom(t *testing.T, body string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/config.yaml", []byte(body), 0o600))

	originalWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(originalWd) })

	EnabledChains = nil
	LoadConfig()
}

const authTestChains = `
chains:
  test12:
    rpc_endpoint: "https://rpc.test12.testnets.gno.land"
    graphql: "https://indexer.test12.testnets.gno.land/graphql/query"
    gnoweb: "https://test12.testnets.gno.land"
    enabled: true
`

// Every currently deployed config.yaml predates the auth_provider key, so an
// absent key must keep the instance on Clerk rather than breaking startup.
func TestLoadConfig_AuthProviderDefaultsToClerk(t *testing.T) {
	loadConfigFrom(t, `
backend_port: "8989"
clerk_secret_key: "test_key"
`+authTestChains)

	assert.Equal(t, AuthProviderClerk, Config.AuthProvider)
	assert.True(t, Config.ClerkFallbackEnabled(), "the Clerk fallback must default on")
}

func TestLoadConfig_KeycloakProvider(t *testing.T) {
	loadConfigFrom(t, `
backend_port: "8989"
auth_provider: "keycloak"
keycloak_issuer: "https://auth.samourai.app/realms/gno-world"
`+authTestChains)

	assert.Equal(t, AuthProviderKeycloak, Config.AuthProvider)
	assert.Equal(t, "https://auth.samourai.app/realms/gno-world", Config.KeycloakIssuer)
	assert.True(t, Config.ClerkFallbackEnabled(), "absent keycloak_clerk_fallback must mean on, not off")
}

// Dropping the Clerk fallback must be an explicit opt-out, since doing it
// before memba and gnolove cut over 401s their live traffic.
func TestLoadConfig_KeycloakClerkFallbackCanBeDisabled(t *testing.T) {
	loadConfigFrom(t, `
backend_port: "8989"
auth_provider: "keycloak"
keycloak_issuer: "https://auth.samourai.app/realms/gno-world"
keycloak_clerk_fallback: false
`+authTestChains)

	assert.False(t, Config.ClerkFallbackEnabled())
}
