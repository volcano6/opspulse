package notify

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// RedactURL masks the credential-bearing part of a webhook URL for display and
// error messages. Webhook tokens live in the path (Slack, Feishu) or in the
// path plus userinfo (Discord), so only scheme and host survive; everything
// else becomes a fixed "<redacted>" placeholder. A value that cannot be parsed
// as an absolute URL is replaced wholesale rather than echoed back.
//
// The redacted form stays stable for the same channel, so operators can still
// tell two channels apart by host — the channel name is the intended
// identifier, and the token is what must not leak into terminals, logs, CI
// output or issue trackers.
func RedactURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<redacted-url>"
	}

	host := parsed.Host
	if parsed.User != nil {
		host = "<redacted>@" + parsed.Host
	}

	// A bare "https://host" carries no secret, so it is shown as-is; any path,
	// query or fragment is treated as credential-shaped and hidden. The
	// placeholder also marks that something was removed.
	if parsed.Path == "" || parsed.Path == "/" {
		if parsed.RawQuery == "" && parsed.Fragment == "" {
			return fmt.Sprintf("%s://%s", parsed.Scheme, host)
		}
	}
	return fmt.Sprintf("%s://%s/<redacted>", parsed.Scheme, host)
}

// redactedError wraps a delivery failure while replacing the channel URL — and
// any URL the underlying error echoes back — with its redacted form. The cause
// is kept so errors.Is/errors.As still work for callers that classify failures
// (context cancellation, DNS, TLS).
type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }

func (e *redactedError) Unwrap() error { return e.cause }

// redactSecretOccurrences replaces every representation of the webhook URL in
// text with a redacted form. Remote responses and net/url errors rarely echo
// the URL byte-for-byte: url.Error renders it with %q (so a control character
// comes back escaped), and a provider rejecting a bad token usually quotes only
// the request path. All of those forms are credential-bearing, so all of them
// are masked — the text is a short diagnostic snippet, and over-redacting it is
// strictly better than leaking a token into a log.
func redactSecretOccurrences(text, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return text
	}

	replacement := RedactURL(trimmed)
	text = strings.ReplaceAll(text, trimmed, replacement)

	if quoted := strconv.Quote(trimmed); len(quoted) >= 2 {
		text = strings.ReplaceAll(text, quoted[1:len(quoted)-1], replacement)
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return text
	}

	if uri := parsed.RequestURI(); uri != "" && uri != "/" {
		text = strings.ReplaceAll(text, uri, "/<redacted>")
	}
	// Some providers echo only the final path segment, which is exactly the
	// token. Short segments are left alone: redacting a word like "hook" would
	// mangle the snippet without protecting anything.
	if last := path.Base(parsed.Path); len(last) >= 6 && last != "/" {
		text = strings.ReplaceAll(text, last, "<redacted>")
	}
	return text
}
