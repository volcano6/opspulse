package main

import (
	"testing"
)

func TestFormatTransferBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
		{5368709120, "5.00 GB"},
	}

	for _, tt := range tests {
		got := formatTransferBytes(tt.bytes)
		if got != tt.want {
			t.Errorf("formatTransferBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
