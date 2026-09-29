package format

import (
	"math"
	"testing"
)

// TestBytes pins the boundary table: the sub-kilobyte path, the unit ladder up
// to the end of the table, and the values at or above 1 EiB where the old
// server-side implementation indexed past the end of its unit table.
func TestBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1 << 50, "1.00 PB"},
		{1 << 60, "1.00 EB"},
		{math.MaxInt64, "8.00 EB"},
	}

	for _, tt := range tests {
		got := Bytes(tt.bytes)
		if got != tt.want {
			t.Errorf("Bytes(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
