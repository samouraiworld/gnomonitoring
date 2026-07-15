package internal

import (
	"strings"
	"testing"
)

func TestRenderAlertDiscordEmbed_TitleColorAndFields(t *testing.T) {
	d := AlertData{
		ChainID: "test12",
		Level:   AlertCritical,
		Emoji:   "🚨",
		Title:   "CRITICAL",
		Date:    "2026-03-19",
		Fields: []AlertField{
			{Name: "addr", Value: "g1addr"},
			{Name: "moniker", Value: "mon1"},
		},
	}
	content, embed := RenderAlertDiscordEmbed(d)

	if content != "" {
		t.Fatalf("content = %q, want empty (no mentions)", content)
	}
	wantTitle := "[test12] 🚨 CRITICAL"
	if embed.Title != wantTitle {
		t.Fatalf("Title = %q, want %q", embed.Title, wantTitle)
	}
	if embed.Color != 0xE74C3C {
		t.Fatalf("Color = %#x, want %#x", embed.Color, 0xE74C3C)
	}
	if len(embed.Fields) != 2 || embed.Fields[0].Name != "addr" || embed.Fields[0].Value != "g1addr" {
		t.Fatalf("Fields = %+v, want addr/moniker fields in order", embed.Fields)
	}
	if embed.Footer == nil || embed.Footer.Text != "2026-03-19" {
		t.Fatalf("Footer = %+v, want date footer", embed.Footer)
	}
}

func TestRenderAlertDiscordEmbed_MentionsGoInContentNotEmbed(t *testing.T) {
	d := AlertData{
		ChainID:  "test12",
		Level:    AlertCritical,
		Emoji:    "🚨",
		Title:    "CRITICAL",
		Mentions: []string{"111", "222"},
	}
	content, embed := RenderAlertDiscordEmbed(d)

	want := "<@111>\n<@222>"
	if content != want {
		t.Fatalf("content = %q, want %q", content, want)
	}
	if strings.Contains(embed.Description, "<@") || strings.Contains(embed.Title, "<@") {
		t.Fatalf("mentions must not leak into the embed itself, got title=%q description=%q", embed.Title, embed.Description)
	}
}

func TestRenderAlertDiscordEmbed_ColorByLevel(t *testing.T) {
	cases := []struct {
		level AlertLevel
		want  int
	}{
		{AlertCritical, 0xE74C3C},
		{AlertWarning, 0xF39C12},
		{AlertResolved, 0x2ECC71},
		{AlertInfo, 0x3498DB},
	}
	for _, c := range cases {
		_, embed := RenderAlertDiscordEmbed(AlertData{ChainID: "test12", Level: c.level, Title: "x"})
		if embed.Color != c.want {
			t.Fatalf("level %s: Color = %#x, want %#x", c.level, embed.Color, c.want)
		}
	}
}

func TestRenderAlertDiscordEmbed_DescriptionPassthrough(t *testing.T) {
	d := AlertData{ChainID: "test12", Level: AlertInfo, Emoji: "✅", Title: "Activity Restored", Description: "Gno.land is back to normal."}
	_, embed := RenderAlertDiscordEmbed(d)
	if embed.Description != "Gno.land is back to normal." {
		t.Fatalf("Description = %q, want passthrough of AlertData.Description", embed.Description)
	}
}

func TestRenderAlertSlackBlocks_HeaderAndFieldsSection(t *testing.T) {
	d := AlertData{
		ChainID: "test12", Emoji: "⚠️", Title: "WARNING",
		Fields: []AlertField{{Name: "addr", Value: "g1addr"}, {Name: "moniker", Value: "mon1"}},
	}
	blocks := RenderAlertSlackBlocks(d)

	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2 (header + fields section)", len(blocks))
	}
	if blocks[0].Type != "header" || blocks[0].Text.Text != "[test12] ⚠️ WARNING" {
		t.Fatalf("header block = %+v", blocks[0])
	}
	wantSection := "*addr*: g1addr\n*moniker*: mon1"
	if blocks[1].Type != "section" || blocks[1].Text.Text != wantSection {
		t.Fatalf("section block = %+v, want text %q", blocks[1], wantSection)
	}
}

func TestRenderAlertSlackBlocks_MentionsInContextBlock(t *testing.T) {
	d := AlertData{ChainID: "test12", Emoji: "🚨", Title: "CRITICAL", Mentions: []string{"111", "222"}}
	blocks := RenderAlertSlackBlocks(d)

	last := blocks[len(blocks)-1]
	if last.Type != "context" {
		t.Fatalf("last block type = %q, want context", last.Type)
	}
	if len(last.Elements) != 1 || last.Elements[0].Text != "<@111> <@222>" {
		t.Fatalf("context elements = %+v", last.Elements)
	}
}

func TestRenderAlertSlackBlocks_DescriptionOnlyNoFields(t *testing.T) {
	d := AlertData{ChainID: "test12", Emoji: "✅", Title: "Activity Restored", Description: "Gno.land is back to normal."}
	blocks := RenderAlertSlackBlocks(d)

	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2 (header + description section)", len(blocks))
	}
	if blocks[1].Text.Text != "Gno.land is back to normal." {
		t.Fatalf("section text = %q", blocks[1].Text.Text)
	}
}

func TestRenderAlertTelegramHTML_TitleAndFields(t *testing.T) {
	d := AlertData{
		ChainID: "test12", Emoji: "🚨", Title: "CRITICAL", Date: "2026-03-19",
		Fields: []AlertField{
			{Name: "addr", Value: "g1addr"},
			{Name: "missed blocks", Value: "5 (1000 -> 1004)"},
		},
	}
	got := RenderAlertTelegramHTML(d)

	want := "<b>[test12] 🚨 CRITICAL</b>\n" +
		"<b>addr</b>: <code>g1addr</code>\n" +
		"<b>missed blocks</b>: 5 (1000 -&gt; 1004)\n" +
		"2026-03-19"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderAlertTelegramHTML_EscapesFieldValues(t *testing.T) {
	d := AlertData{
		ChainID: "test12", Emoji: "⚠️", Title: "WARNING",
		Fields: []AlertField{{Name: "moniker", Value: "<script>"}},
	}
	got := RenderAlertTelegramHTML(d)
	if strings.Contains(got, "<script>") {
		t.Fatalf("moniker must be HTML-escaped, got:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("expected escaped moniker, got:\n%s", got)
	}
}

func TestRenderAlertTelegramHTML_DescriptionNoFields(t *testing.T) {
	d := AlertData{ChainID: "test12", Emoji: "✅", Title: "Activity Restored", Description: "Gno.land is back to normal."}
	got := RenderAlertTelegramHTML(d)
	want := "<b>[test12] ✅ Activity Restored</b>\nGno.land is back to normal."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
