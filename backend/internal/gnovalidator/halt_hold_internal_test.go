package gnovalidator

import (
	"context"
	"testing"
	"time"

	"github.com/samouraiworld/gnomonitoring/backend/internal/chainquery"
	"github.com/samouraiworld/gnomonitoring/backend/internal/database"
	"github.com/samouraiworld/gnomonitoring/backend/internal/govdao"
	"github.com/samouraiworld/gnomonitoring/backend/internal/testoutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectParticipation_GovDAOHaltHoldsStuckAlert checks a chain stopped
// at a halt GovDAO scheduled, and announced, raises no "Blockchain stuck"
// alert, while a stop at any other height still does.
func TestCollectParticipation_GovDAOHaltHoldsStuckAlert(t *testing.T) {
	for _, tc := range []struct {
		name       string
		haltHeight int64
		resumed    bool
		wantAlert  bool
	}{
		{name: "halt at the stopped height", haltHeight: 3, wantAlert: false},
		{name: "halt at another height", haltHeight: 4, wantAlert: true},
		{name: "halt at the stopped height, already resumed", haltHeight: 3, resumed: true, wantAlert: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testoutils.NewTestDB(t)
			setStagnationFirstAlertSecondsForTest(t, 1)

			chainID := "test-halt-hold-" + tc.name
			now := time.Now()
			require.NoError(t, database.InsertAlertlog(db, chainID, "halt", "v1.9.0", govdao.LevelHaltScheduled,
				tc.haltHeight, 2, true, now, "scheduled"))
			if tc.resumed {
				require.NoError(t, database.InsertAlertlog(db, chainID, "halt", "v1.9.0", govdao.LevelHaltResumed,
					tc.haltHeight, 3, true, now.Add(time.Millisecond), "resumed"))
			}

			client := newStagnationTestClient([]int64{3, 3, 3})
			ctx, cancel := context.WithCancel(context.Background())
			defer func() {
				cancel()
				// Let the monitor goroutine exit before the next case
				// touches the shared package state.
				time.Sleep(300 * time.Millisecond)
			}()
			CollectParticipation(ctx, db, chainID, chainquery.Client{RPCClient: client})

			if tc.wantAlert {
				require.Eventually(t, func() bool { return countCriticalStuckAlerts(t, db, chainID) >= 1 },
					20*time.Second, 20*time.Millisecond, "a stop outside an announced halt must raise 'Blockchain stuck'")
				return
			}
			require.Eventually(t, func() bool { return client.callCount() >= 4 }, 20*time.Second, 20*time.Millisecond,
				"the scripted heights must be fully consumed")
			assert.Equal(t, int64(0), countCriticalStuckAlerts(t, db, chainID),
				"a stop at an announced GovDAO halt must not raise 'Blockchain stuck'")
			assert.False(t, IsAlertSent(chainID, "all"))
			// The hold branch ran: it restarts the repeat timer without
			// sending, so the test cannot pass on a monitor that never
			// reached the alert check.
			timeMu.Lock()
			held := lastStagnationAlertTime[chainID]
			timeMu.Unlock()
			assert.False(t, held.IsZero(), "the stuck alert must have been held, not left unchecked")
		})
	}
}
