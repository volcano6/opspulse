package sftp

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	progressMu  sync.Mutex
	progressOut io.Writer = os.Stdout
)

// progressReader wraps an io.Reader and prints a progress bar to stdout
// for files larger than 1MB.
type progressReader struct {
	io.Reader
	total      int64
	current    int64
	filename   string
	direction  string
	target     string
	lastUpdate time.Time
	startTime  time.Time
	active     bool
}

func newProgressReader(r io.Reader, total int64, direction, sourcePath, targetPath string) *progressReader {
	return &progressReader{
		Reader:    r,
		total:     total,
		filename:  filepath.Base(sourcePath),
		direction: direction,
		target:    targetPath,
		startTime: time.Now(),
		active:    total > 1024*1024, // Only activate for files > 1MB
	}
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	pr.current += int64(n)

	if !pr.active {
		return n, err
	}

	now := time.Now()
	// Update terminal at most every 200ms or on completion
	if err == io.EOF || now.Sub(pr.lastUpdate) >= 200*time.Millisecond {
		pr.lastUpdate = now
		progressMu.Lock()
		pr.printProgressLocked()
		if err == io.EOF {
			_, _ = fmt.Fprintln(progressOut)
		}
		progressMu.Unlock()
	}
	return n, err
}

func (pr *progressReader) printProgressLocked() {
	percent := 0.0
	if pr.total > 0 {
		percent = float64(pr.current) / float64(pr.total) * 100
	}
	if percent > 100 {
		percent = 100
	}

	bars := int(percent / 5) // 20 blocks
	barStr := strings.Repeat("=", bars)
	if bars < 20 {
		barStr += ">"
		barStr += strings.Repeat(" ", 19-bars)
	}

	elapsed := time.Since(pr.startTime).Seconds()
	speed := 0.0
	if elapsed > 0 {
		speed = float64(pr.current) / elapsed / 1024 / 1024 // MB/s
	}

	// \033[K clears the rest of the line
	_, _ = fmt.Fprintf(progressOut, "\r%s %s → %s [%s] %3.0f%%  %.1f MB/s\033[K",
		pr.direction, pr.filename, pr.target, barStr, percent, speed)
}
