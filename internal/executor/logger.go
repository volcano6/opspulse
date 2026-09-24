package executor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/volcano6/opspulse/internal/config"
)

// LogPathFor generates the standard log file path for a server operation and ensures the directory exists.
//
// The name arrives from three callers: bootstrap's server name, and the backup
// and restore runners' job names (already prefixed with "backup-"/"restore-").
// Job names are validated by internal/backup, but this is the single place that
// turns an arbitrary string into a file path, so it sanitises instead of
// trusting its callers: a name carrying a separator or ".." must never be able
// to place a log file outside logDir.
func LogPathFor(serverName string, timestamp time.Time) (string, error) {
	logDir := filepath.Join(config.DataDir(), "logs")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create logs directory: %w", err)
	}

	fileName := fmt.Sprintf("bootstrap-%s-%s.log", sanitizeLogSegment(serverName), timestamp.Format("20060102T150405"))
	path := filepath.Join(logDir, fileName)

	// Belt and braces: sanitizeLogSegment already removes separators, so a
	// result outside logDir means a bug in it rather than hostile input. Better
	// to fail than to write somewhere unexpected.
	if rel, err := filepath.Rel(logDir, path); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to place the log file outside %s", logDir)
	}
	return path, nil
}

// sanitizeLogSegment reduces s to something safe as a single path segment.
//
// Separators, NUL and control characters become "_", leading dots are dropped
// so the name can never be "." or "..", and the result is capped in runes so a
// long name cannot push the path past the filesystem's limit. It deliberately
// does not try to whitelist characters: job and server names carry Unicode, and
// the only property that matters here is "cannot escape its directory".
func sanitizeLogSegment(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '/' || r == '\\' || r == 0 || unicode.IsControl(r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}

	out := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	if out == "" {
		return "unnamed"
	}
	if runes := []rune(out); len(runes) > maxLogSegmentRunes {
		out = string(runes[:maxLogSegmentRunes])
	}
	return out
}

// maxLogSegmentRunes caps the caller-supplied part of a log file name. Server
// and job names are far shorter than this in practice; the cap exists only so a
// pathological name cannot produce a path the filesystem rejects.
const maxLogSegmentRunes = 96

// PrefixedWriter prefixes every new line of output with a tag (e.g. "[vps-01] ").
type PrefixedWriter struct {
	Prefix []byte
	Writer io.Writer
	mu     sync.Mutex
	buf    bytes.Buffer
}

// NewPrefixedWriter creates a new PrefixedWriter with the given prefix string.
func NewPrefixedWriter(prefix string, w io.Writer) *PrefixedWriter {
	return &PrefixedWriter{
		Prefix: []byte(prefix),
		Writer: w,
	}
}

// Write writes p to the underlying writer, prefixing lines.
func (pw *PrefixedWriter) Write(p []byte) (n int, err error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	total := len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx >= 0 {
			line := p[:idx+1]
			p = p[idx+1:]

			if pw.buf.Len() > 0 {
				pw.buf.Write(line)
				combined := append([]byte{}, pw.Prefix...)
				combined = append(combined, pw.buf.Bytes()...)
				if _, writeErr := pw.Writer.Write(combined); writeErr != nil {
					return 0, writeErr
				}
				pw.buf.Reset()
			} else {
				combined := append([]byte{}, pw.Prefix...)
				combined = append(combined, line...)
				if _, writeErr := pw.Writer.Write(combined); writeErr != nil {
					return 0, writeErr
				}
			}
		} else {
			pw.buf.Write(p)
			break
		}
	}
	return total, nil
}

// Flush writes any remaining buffered data without a trailing newline.
func (pw *PrefixedWriter) Flush() error {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	if pw.buf.Len() > 0 {
		combined := append([]byte{}, pw.Prefix...)
		combined = append(combined, pw.buf.Bytes()...)
		if _, err := pw.Writer.Write(combined); err != nil {
			return err
		}
		pw.buf.Reset()
		return nil
	}
	return nil
}
