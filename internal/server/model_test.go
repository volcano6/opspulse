package server

import (
	"errors"
	"strings"
	"testing"
)

func TestServer_ValidatePort(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		want    int
		wantErr bool
	}{
		{name: "unset defaults to 22", port: 0, want: 22},
		{name: "explicit port kept", port: 2222, want: 2222},
		{name: "negative port rejected", port: -1, wantErr: true},
		{name: "port above TCP range rejected", port: 65536, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := Server{Name: "srv", Host: "example.com", Port: tc.port}
			err := s.Validate()
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidPort) {
					t.Fatalf("Validate() error = %v, want ErrInvalidPort", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if s.Port != tc.want {
				t.Errorf("Port = %d, want %d", s.Port, tc.want)
			}
		})
	}
}

func TestServer_ValidateRejectsControlCharacters(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Server)
		field  string
	}{
		{
			name: "host newline",
			mutate: func(s *Server) {
				s.Host = "example.com\nProxyCommand touch pwned"
			},
			field: "host",
		},
		{
			name: "host carriage return",
			mutate: func(s *Server) {
				s.Host = "example.com\rHost evil"
			},
			field: "host",
		},
		{
			name: "host NUL",
			mutate: func(s *Server) {
				s.Host = "example.com\x00evil"
			},
			field: "host",
		},
		{
			name: "user tab",
			mutate: func(s *Server) {
				s.User = "root\troot"
			},
			field: "user",
		},
		{
			name: "key path newline",
			mutate: func(s *Server) {
				s.KeyPath = "/tmp/key\nIdentityFile /tmp/evil"
			},
			field: "key_path",
		},
		{
			name: "jump host carriage return",
			mutate: func(s *Server) {
				s.JumpHost = "bastion\rProxyJump evil"
			},
			field: "jump_host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Server{Name: "srv", Host: "example.com"}
			tt.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want a control-character error")
			}
			if !strings.Contains(err.Error(), tt.field) || !strings.Contains(err.Error(), "control character") {
				t.Fatalf("Validate() error = %q, want it to identify %q", err, tt.field)
			}
		})
	}
}

func TestServer_ValidateRejectsHostSurroundingWhitespace(t *testing.T) {
	for _, host := range []string{" example.com", "example.com ", "\texample.com"} {
		t.Run(host, func(t *testing.T) {
			s := Server{Name: "srv", Host: host}
			if err := s.Validate(); !errors.Is(err, ErrInvalidHost) {
				t.Fatalf("Validate() error = %v, want ErrInvalidHost", err)
			}
		})
	}
}

func TestServer_ValidateRejectsRoutingFieldSurroundingWhitespace(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Server)
		field  string
	}{
		{
			name: "user",
			mutate: func(s *Server) {
				s.User = " root"
			},
			field: "user",
		},
		{
			name: "jump host",
			mutate: func(s *Server) {
				s.JumpHost = "bastion "
			},
			field: "jump_host",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Server{Name: "srv", Host: "example.com"}
			tt.mutate(&s)
			err := s.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.field) || !strings.Contains(err.Error(), "whitespace") {
				t.Fatalf("Validate() error = %v, want a %s whitespace error", err, tt.field)
			}
		})
	}
}

func TestServer_HasTag(t *testing.T) {
	s := Server{
		Name: "test-srv",
		Tags: []string{"prod", "web", "LEGACY-SSH"},
	}

	if !s.HasTag("prod") {
		t.Error("expected HasTag('prod') to be true")
	}
	if !s.HasTag("WEB") {
		t.Error("expected HasTag('WEB') case-insensitive to be true")
	}
	if !s.HasTag("legacy-ssh") {
		t.Error("expected HasTag('legacy-ssh') case-insensitive to be true")
	}
	if s.HasTag("database") {
		t.Error("expected HasTag('database') to be false")
	}

	if s.HasTag("") {
		t.Error("expected HasTag('') with empty string to be false")
	}
	if s.HasTag("   ") {
		t.Error("expected HasTag('   ') with whitespace to be false")
	}

	var nilServer *Server
	if nilServer.HasTag("prod") {
		t.Error("expected nil Server.HasTag() to be false")
	}
}

func TestServer_IsLegacySSH(t *testing.T) {
	tests := []struct {
		name string
		srv  *Server
		want bool
	}{
		{
			name: "nil server",
			srv:  nil,
			want: false,
		},
		{
			name: "modern server without tags",
			srv: &Server{
				Name: "modern",
				Tags: []string{"prod", "web"},
			},
			want: false,
		},
		{
			name: "server with legacy-ssh tag",
			srv: &Server{
				Name: "legacy-1",
				Tags: []string{"legacy-ssh"},
			},
			want: true,
		},
		{
			name: "server with legacy_ssh tag",
			srv: &Server{
				Name: "legacy-2",
				Tags: []string{"legacy_ssh"},
			},
			want: true,
		},
		{
			name: "server with legacy-rsa tag",
			srv: &Server{
				Name: "legacy-3",
				Tags: []string{"legacy-rsa"},
			},
			want: true,
		},
		{
			name: "server with legacy-ssh label true",
			srv: &Server{
				Name:   "legacy-4",
				Labels: map[string]string{"legacy-ssh": "true"},
			},
			want: true,
		},
		{
			name: "server with legacy-rsa label true",
			srv: &Server{
				Name:   "legacy-4-rsa",
				Labels: map[string]string{"legacy-rsa": "true"},
			},
			want: true,
		},
		{
			name: "server with legacy_ssh label 1",
			srv: &Server{
				Name:   "legacy-4-yes",
				Labels: map[string]string{"legacy_ssh": "1"},
			},
			want: true,
		},
		{
			name: "server with legacy-ssh label false",
			srv: &Server{
				Name:   "legacy-5",
				Labels: map[string]string{"legacy-ssh": "false"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.srv.IsLegacySSH(); got != tt.want {
				t.Errorf("IsLegacySSH() = %v, want %v", got, tt.want)
			}
		})
	}
}
