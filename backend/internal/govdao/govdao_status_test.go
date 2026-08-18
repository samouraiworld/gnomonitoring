package govdao

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// renderPage builds a proposal page render mirroring the layout of
// gno.land/r/gov/dao/v3/impl's renderProposalPage: a title/author/description
// header, the proposalStatus.String() "### Stats" block, then the action bar.
//
// The action bar matters: the realm emits it unconditionally, on accepted and
// denied proposals alike. The pre-fix parser keyed off its "Vote YES" string
// and therefore reported every rejected proposal as IN PROGRESS.
func renderPage(description, statsMarker string) string {
	return "## Prop #0 - Onboard validator4\n" +
		"Author: g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5\n\n" +
		description + "\n\n" +
		"\n\n---\n\n" +
		"### Stats\n" +
		statsMarker +
		"- Tiers eligible to vote: T1, T2, T3\n" +
		"- YES PERCENT: 0%\n" +
		"- NO PERCENT: 100%\n" +
		"\n" +
		"[Detailed voting list](/r/gov/dao:0/votes)\n\n---\n\n" +
		"### Actions\n" +
		"[Vote YES](/r/gov/dao$help&func=MustVoteOnProposalSimple&pid=0&option=YES) | " +
		"[Vote NO](/r/gov/dao$help&func=MustVoteOnProposalSimple&pid=0&option=NO) | " +
		"[Vote ABSTAIN](/r/gov/dao$help&func=MustVoteOnProposalSimple&pid=0&option=ABSTAIN)\n\n" +
		"WARNING: Please double check transaction data before voting.\n"
}

func TestParseProposalStatus(t *testing.T) {
	tests := []struct {
		name   string
		render string
		want   string
	}{
		{
			name:   "accepted proposal",
			render: renderPage("Onboard samourai-crew-4.", "- **PROPOSAL HAS BEEN ACCEPTED**\n"),
			want:   "ACCEPTED",
		},
		{
			name:   "denied proposal",
			render: renderPage("Onboard samourai-crew-4.", "- **PROPOSAL HAS BEEN DENIED**\n"),
			want:   "REJECTED",
		},
		{
			// The realm appends the reason without a trailing newline, so it
			// runs into the following "- Tiers eligible" line. The marker line
			// itself is still intact, which is what the parser keys off.
			name:   "denied proposal carrying a reason",
			render: renderPage("Onboard samourai-crew-4.", "- **PROPOSAL HAS BEEN DENIED**\nREASON: quorum not reached"),
			want:   "REJECTED",
		},
		{
			name:   "proposal still open for votes",
			render: renderPage("Onboard samourai-crew-4.", "- **Proposal is open for votes**\n"),
			want:   "IN PROGRESS",
		},
		{
			// Regression guard for the pre-fix strings.Contains("ACCEPTED"):
			// the description is rendered on the page, so a proposal merely
			// mentioning the word must not be read as accepted.
			name:   "open proposal whose description mentions ACCEPTED",
			render: renderPage("Revert the previously ACCEPTED onboarding of validator4.", "- **Proposal is open for votes**\n"),
			want:   "IN PROGRESS",
		},
		{
			// Same trap in the other direction: a denied proposal whose text
			// mentions ACCEPTED must still parse as REJECTED.
			name:   "denied proposal whose description mentions ACCEPTED",
			render: renderPage("Revert the previously ACCEPTED onboarding of validator4.", "- **PROPOSAL HAS BEEN DENIED**\n"),
			want:   "REJECTED",
		},
		{
			name:   "empty render",
			render: "",
			want:   "UNKNOWN",
		},
		{
			// An unrecognised page (realm error, different gov/dao version,
			// gnoweb error page) must never be read as a terminal state.
			name:   "unrecognised render",
			render: "unknown request path: 42\n",
			want:   "UNKNOWN",
		},
		{
			name:   "realm panic message",
			render: "panic: runtime error: index out of range\n",
			want:   "UNKNOWN",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseProposalStatus(tt.render))
		})
	}
}

// TestIsTerminalProposalStatus pins which statuses may trigger a notification
// and a stored status transition. UNKNOWN must never qualify: it is what the
// parser returns when it could not read the page at all.
func TestIsTerminalProposalStatus(t *testing.T) {
	assert.True(t, isTerminalProposalStatus("ACCEPTED"))
	assert.True(t, isTerminalProposalStatus("REJECTED"))
	assert.False(t, isTerminalProposalStatus("IN PROGRESS"))
	assert.False(t, isTerminalProposalStatus("UNKNOWN"))
	assert.False(t, isTerminalProposalStatus(""))
}
