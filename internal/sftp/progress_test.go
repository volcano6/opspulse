package sftp

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestProgressOutputIsSerialized(t *testing.T) {
	var output bytes.Buffer
	progressMu.Lock()
	oldOut := progressOut
	progressOut = &output
	progressMu.Unlock()
	defer func() {
		progressMu.Lock()
		progressOut = oldOut
		progressMu.Unlock()
	}()

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader := newProgressReader(bytes.NewReader(make([]byte, 2*1024*1024)), 2*1024*1024, "Uploading", "file", "target")
			_, _ = io.Copy(io.Discard, reader)
		}()
	}
	wg.Wait()

	for _, segment := range strings.Split(output.String(), "\r")[1:] {
		if strings.Count(segment, "%") != 1 || strings.Count(segment, "→") != 1 {
			t.Errorf("interleaved progress segment = %q", segment)
		}
	}
}
