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
