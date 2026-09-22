package secret

import (
	"strings"
	"testing"
)

func TestRejectLegacy1PRef(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		val        string
		serverName string
		wantErr    bool
	}{
		{name: "op reference is rejected", field: "key_path", val: "op://Private/web/key", serverName: "web", wantErr: true},
		{name: "op reference with surrounding space is rejected", field: "password", val: "  op://Private/web/password ", serverName: "web", wantErr: true},
		{name: "local path is accepted", field: "key_path", val: "~/.ssh/opspulse_web", serverName: "web"},
		{name: "plaintext password is accepted", field: "password", val: "hunter2", serverName: "web"},
		{name: "empty value is accepted", field: "password", val: "", serverName: "web"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RejectLegacy1PRef(tt.field, tt.val, tt.serverName)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("RejectLegacy1PRef() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("RejectLegacy1PRef() = nil, want an error")
			}
			for _, want := range []string{tt.serverName, tt.field, "ops 1p restore"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("RejectLegacy1PRef() = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}
