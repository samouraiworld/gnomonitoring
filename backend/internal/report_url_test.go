package internal

import "testing"

func TestValidateReportURL(t *testing.T) {
	valid := []string{
		"",
		"https://memba.club/os/validators",
		"http://localhost:3000/reports/{chain}",
		"https://memba.club/os/reports/{chain}?tab=score",
	}
	for _, v := range valid {
		if err := ValidateReportURL(v); err != nil {
			t.Errorf("ValidateReportURL(%q) = %v, want nil", v, err)
		}
	}

	invalid := []string{
		"memba.club/os/validators",            // no scheme
		"javascript:alert(1)",                 // not http(s)
		"https:///validators",                 // no host
		"https://example.test/<b>",            // Telegram HTML
		"https://example.test/a?b=1&c=2",      // Telegram HTML entity
		"https://example.test/a|click here",   // Slack link label
		"https://example.test/\"onmouseover=", // quote
		"https://example.test/a b",            // whitespace
	}
	for _, v := range invalid {
		if err := ValidateReportURL(v); err == nil {
			t.Errorf("ValidateReportURL(%q) = nil, want an error", v)
		}
	}
}
