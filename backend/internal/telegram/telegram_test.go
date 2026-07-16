package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendMessageTelegramWithLinkButton_IncludesURLButton(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	restore := telegramAPIBaseURL
	telegramAPIBaseURL = srv.URL
	defer func() { telegramAPIBaseURL = restore }()

	err := SendMessageTelegramWithLinkButton("tok", 42, "hello", "View report", "https://example.com/report")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	markup, ok := captured["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing or wrong type, got: %+v", captured)
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("inline_keyboard missing or wrong shape, got: %+v", markup)
	}
	row, ok := rows[0].([]any)
	if !ok || len(row) != 1 {
		t.Fatalf("inline_keyboard row missing or wrong shape, got: %+v", rows[0])
	}
	button, ok := row[0].(map[string]any)
	if !ok {
		t.Fatalf("button missing or wrong type, got: %+v", row[0])
	}
	if button["text"] != "View report" {
		t.Fatalf("button text = %v, want %q", button["text"], "View report")
	}
	if button["url"] != "https://example.com/report" {
		t.Fatalf("button url = %v, want %q", button["url"], "https://example.com/report")
	}
}

func TestSendMessageTelegramWithLinkButton_NoButtonWhenURLEmpty(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	restore := telegramAPIBaseURL
	telegramAPIBaseURL = srv.URL
	defer func() { telegramAPIBaseURL = restore }()

	err := SendMessageTelegramWithLinkButton("tok", 42, "hello", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := captured["reply_markup"]; present {
		t.Fatalf("reply_markup must be absent when buttonURL is empty, got: %+v", captured)
	}
}
