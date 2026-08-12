package gnovalidator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestShouldSendRPCAlert_FirstOutageAlwaysAlerts(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
}

func TestShouldSendRPCAlert_SuppressedInsideCooldown(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	assert.False(t, shouldSendRPCAlert("chain-a", now.Add(9*time.Minute), 10*time.Minute))
	assert.True(t, shouldSendRPCAlert("chain-a", now.Add(11*time.Minute), 10*time.Minute))
}

func TestShouldSendRPCAlert_IsPerChain(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	assert.True(t, shouldSendRPCAlert("chain-b", now, 10*time.Minute),
		"one chain's RPC outage must not mute another chain's alert")
}

func TestClearRPCAlert_ReArmsTheGate(t *testing.T) {
	resetRPCAlertState()
	now := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	assert.True(t, shouldSendRPCAlert("chain-a", now, 10*time.Minute))
	clearRPCAlert("chain-a")
	assert.True(t, shouldSendRPCAlert("chain-a", now.Add(time.Minute), 10*time.Minute),
		"a recovery must let the next outage alert immediately")
}
