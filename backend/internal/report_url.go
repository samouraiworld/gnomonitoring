package internal

import (
	"errors"
	"net/url"
	"strings"
)

// ReportURLForbiddenChars are characters that carry markup meaning in at
// least one notification channel: Telegram messages use HTML parse mode,
// Slack treats <url|text> as a link, and quotes or whitespace would end the
// URL early. report_url is inserted verbatim into those messages.
const ReportURLForbiddenChars = "<>\"'`&| \t\r\n"

// MaxReportURLLen keeps the link well inside Discord's 1024-char field value
// limit, so a long URL can never make Discord reject the daily report.
const MaxReportURLLen = 512

// ValidateReportURL accepts an empty value (link disabled) or an absolute
// http(s) URL that cannot break the formatting of the messages it is
// inserted into. A {chain} placeholder is allowed anywhere.
func ValidateReportURL(v string) error {
	if v == "" {
		return nil
	}
	if len(v) > MaxReportURLLen {
		return errors.New("must be at most 512 characters")
	}
	if strings.ContainsAny(v, ReportURLForbiddenChars) {
		return errors.New("must not contain spaces or any of < > \" ' ` & |")
	}
	u, err := url.Parse(strings.ReplaceAll(v, "{chain}", "chain"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an absolute http:// or https:// URL")
	}
	return nil
}
