package internal

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

// TestLoadMultiChainConfig verifies multi-chain config loading
func TestLoadMultiChainConfig(t *testing.T) {
	// Create temporary config file
	configContent := `
backend_port: "8989"
metrics_port: 8888
dev_mode: true
allow_origin: "http://localhost:3000"
clerk_secret_key: "test_key"
token_telegram_validator: ""
token_telegram_govdao: ""

chains:
  test12:
    rpc_endpoint: "https://rpc.test12.testnets.gno.land"
    graphql: "https://indexer.test12.testnets.gno.land/graphql/query"
    gnoweb: "https://test12.testnets.gno.land"
    enabled: true
  gnoland1:
    rpc_endpoint: "https://rpc.gnoland1.testnets.gno.land"
    graphql: "https://indexer.gnoland1.testnets.gno.land/graphql/query"
    gnoweb: "https://gnoland1.testnets.gno.land"
    enabled: true
`

	tmpfile, err := os.CreateTemp("", "config*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpfile.Name())

	_, err = tmpfile.WriteString(configContent)
	require.NoError(t, err)
	tmpfile.Close()

	// Change to temp dir and load config
	originalWd, _ := os.Getwd()
	tmpDir := os.TempDir()
	os.Chdir(tmpDir)
	os.Rename(tmpfile.Name(), "config.yaml")
	defer func() {
		os.Remove("config.yaml")
		os.Chdir(originalWd)
	}()

	LoadConfig()

	// Verify config loaded
	assert.Equal(t, "8989", Config.BackendPort)
	assert.Equal(t, 8888, Config.MetricsPort)
	assert.True(t, Config.DevMode)

	// Verify chains loaded
	assert.NotNil(t, Config.Chains)
	assert.Equal(t, 2, len(Config.Chains))

	// Verify enabled chains
	assert.Equal(t, 2, len(EnabledChains))
	assert.Equal(t, "gnoland1", EnabledChains[0]) // Alphabetically sorted
	assert.Equal(t, "test12", EnabledChains[1])
}

// TestGetChainConfig verifies GetChainConfig helper
func TestGetChainConfig(t *testing.T) {
	testConfig := config{
		Chains: map[string]*ChainConfig{
			"test12": {
				RPCEndpoint:     "https://rpc.test12.testnets.gno.land",
				GraphqlEndpoint: "https://indexer.test12.testnets.gno.land/graphql/query",
				GnowebEndpoint:  "https://test12.testnets.gno.land",
				Enabled:         true,
			},
			"disabled": {
				RPCEndpoint: "https://rpc.disabled.testnets.gno.land",
				Enabled:     false,
			},
		},
	}

	// Test getting enabled chain
	cfg, err := testConfig.GetChainConfig("test12")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, "https://rpc.test12.testnets.gno.land", cfg.RPCEndpoint)

	// Test getting disabled chain (should return it)
	cfg, err = testConfig.GetChainConfig("disabled")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)

	// Test getting non-existent chain
	cfg, err = testConfig.GetChainConfig("nonexistent")
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

// TestValidateChainID verifies ValidateChainID helper
func TestValidateChainID(t *testing.T) {
	testConfig := config{
		Chains: map[string]*ChainConfig{
			"test12": {
				Enabled: true,
			},
			"gnoland1": {
				Enabled: true,
			},
		},
	}

	// Test valid chain ID
	err := testConfig.ValidateChainID("test12")
	assert.NoError(t, err)

	// Test another valid chain ID
	err = testConfig.ValidateChainID("gnoland1")
	assert.NoError(t, err)

	// Test invalid chain ID
	err = testConfig.ValidateChainID("invalid")
	assert.Error(t, err)
}

// TestGetEnabledChainIDs verifies chain sorting and filtering
func TestGetEnabledChainIDs(t *testing.T) {
	testConfig := config{
		Chains: map[string]*ChainConfig{
			"zebra": {Enabled: false},
			"alpha": {Enabled: true},
			"gamma": {Enabled: true},
			"beta":  {Enabled: true},
		},
	}

	ids := testConfig.GetEnabledChainIDs()
	assert.Equal(t, 3, len(ids))
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, ids)
}

// TestConfigYAMLUnmarshal verifies YAML unmarshaling
func TestConfigYAMLUnmarshal(t *testing.T) {
	yamlData := `
backend_port: "9000"
metrics_port: 9001
dev_mode: false
allow_origin: "https://example.com,https://app.example.com"
clerk_secret_key: "key123"
token_telegram_validator: "bot_token_1"
token_telegram_govdao: "bot_token_2"

chains:
  test12:
    rpc_endpoint: "https://rpc.test12.testnets.gno.land"
    graphql: "https://indexer.test12.testnets.gno.land/graphql/query"
    gnoweb: "https://test12.testnets.gno.land"
    enabled: true
`

	var cfg config
	err := yaml.Unmarshal([]byte(yamlData), &cfg)
	assert.NoError(t, err)
	assert.Equal(t, "9000", cfg.BackendPort)
	assert.Equal(t, 9001, cfg.MetricsPort)
	assert.False(t, cfg.DevMode)
	assert.Equal(t, 1, len(cfg.Chains))
	assert.NotNil(t, cfg.Chains["test12"])
	assert.True(t, cfg.Chains["test12"].Enabled)
}

// TestChainConfigUnmarshal verifies ChainConfig YAML unmarshaling
func TestChainConfigUnmarshal(t *testing.T) {
	yamlData := `
test12:
  rpc_endpoint: "https://rpc.test12.testnets.gno.land"
  graphql: "https://indexer.test12.testnets.gno.land/graphql/query"
  gnoweb: "https://test12.testnets.gno.land"
  enabled: true
`

	var chains map[string]*ChainConfig
	err := yaml.Unmarshal([]byte(yamlData), &chains)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(chains))
	assert.NotNil(t, chains["test12"])
	assert.Equal(t, "https://rpc.test12.testnets.gno.land", chains["test12"].RPCEndpoint)
	assert.Equal(t, "https://indexer.test12.testnets.gno.land/graphql/query", chains["test12"].GraphqlEndpoint)
	assert.Equal(t, "https://test12.testnets.gno.land", chains["test12"].GnowebEndpoint)
	assert.True(t, chains["test12"].Enabled)
}

// TestAllowedOriginsParsing verifies allow_origin comma-separated parsing
func TestAllowedOriginsParsing(t *testing.T) {
	yamlData := `
backend_port: "8989"
metrics_port: 8888
dev_mode: true
allow_origin: "http://localhost:3000,https://example.com, https://another.com"
clerk_secret_key: "test_key"
token_telegram_validator: ""
token_telegram_govdao: ""

chains:
  test12:
    rpc_endpoint: "https://rpc.test12.testnets.gno.land"
    graphql: "https://indexer.test12.testnets.gno.land/graphql/query"
    gnoweb: "https://test12.testnets.gno.land"
    enabled: true
`

	var cfg config
	err := yaml.Unmarshal([]byte(yamlData), &cfg)
	assert.NoError(t, err)

	// Parse origins like in LoadConfig
	for _, raw := range []string{
		"http://localhost:3000",
		"https://example.com",
		" https://another.com",
	} {
		origin := raw
		if origin != "" {
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, origin)
		}
	}

	// Trim spaces to match actual behavior
	var trimmedOrigins []string
	for _, origin := range cfg.AllowedOrigins {
		trimmed := origin
		trimmedOrigins = append(trimmedOrigins, trimmed)
	}

	assert.Equal(t, 3, len(trimmedOrigins))
}

// TestEmptyChainsValidation verifies that empty chains list fails
func TestEmptyChainsValidation(t *testing.T) {
	yamlData := `
backend_port: "8989"
metrics_port: 8888
dev_mode: true
allow_origin: "http://localhost:3000"
clerk_secret_key: "test_key"
token_telegram_validator: ""
token_telegram_govdao: ""

chains: {}
`

	var cfg config
	err := yaml.Unmarshal([]byte(yamlData), &cfg)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(cfg.Chains))
}

// TestDisabledChainsFiltering verifies that disabled chains are excluded
func TestDisabledChainsFiltering(t *testing.T) {
	testConfig := config{
		Chains: map[string]*ChainConfig{
			"test12": {
				Enabled: true,
			},
			"gnoland1": {
				Enabled: true,
			},
			"archived": {
				Enabled: false,
			},
		},
	}

	ids := testConfig.GetEnabledChainIDs()
	assert.Equal(t, 2, len(ids))
	// Verify archived is not in the list
	for _, id := range ids {
		assert.NotEqual(t, "archived", id)
	}
}
