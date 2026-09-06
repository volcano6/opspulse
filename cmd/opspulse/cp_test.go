package main

import (
	"testing"
)

func TestParseRemotePath(t *testing.T) {
	tests := []struct {
		input      string
		wantServer string
		wantPath   string
		wantRemote bool
	}{
		{"vps-1:/var/log/nginx.log", "vps-1", "/var/log/nginx.log", true},
		{"web-01:config.yaml", "web-01", "config.yaml", true},
		{"./local/path/file.txt", "", "./local/path/file.txt", false},
		{"/var/log/nginx.log", "", "/var/log/nginx.log", false},
		{`C:\Users\test\file.txt`, "", `C:\Users\test\file.txt`, false},
		{"C:/Users/test/file.txt", "", "C:/Users/test/file.txt", false},
		{"D:\\", "", "D:\\", false},
		{":invalid", "", ":invalid", false},
		{"", "", "", false},
	}

	for _, tt := range tests {
		server, path, isRemote := parseRemotePath(tt.input)
		if isRemote != tt.wantRemote || server != tt.wantServer || path != tt.wantPath {
			t.Errorf("parseRemotePath(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, server, path, isRemote, tt.wantServer, tt.wantPath, tt.wantRemote)
		}
	}
}

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
