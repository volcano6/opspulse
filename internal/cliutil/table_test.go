package cliutil

import (
	"bytes"
	"testing"
)

// TestNewTabWriter pins the shared column layout: padding of three spaces,
// tab-aligned columns, and multi-column alignment across rows of unequal width.
func TestNewTabWriter(t *testing.T) {
	var buf bytes.Buffer
	tw := NewTabWriter(&buf)

	_, _ = tw.Write([]byte("NAME\tHOST\tTAGS\n"))
	_, _ = tw.Write([]byte("web\t192.0.2.10\tprod,edge\n"))
	_, _ = tw.Write([]byte("db-long-name\t198.51.100.20\tprod\n"))

	if err := tw.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	want := "NAME           HOST            TAGS\n" +
		"web            192.0.2.10      prod,edge\n" +
		"db-long-name   198.51.100.20   prod\n"
	if got := buf.String(); got != want {
		t.Errorf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}
