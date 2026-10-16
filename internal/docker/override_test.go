package docker

import (
	"strings"
	"testing"
)

const overrideFixture = `services:
  db:
    image: mysql:8.0
    ports:
      - "3306:3306"
    volumes:
      - blog-data:/var/lib/mysql
      - /srv/blog/conf.d:/etc/mysql/conf.d:ro
  web:
    image: nginx:alpine
    ports:
      - "80:80"
`

func TestOverrideComposeReplacesPortsAndBindPaths(t *testing.T) {
	out, err := OverrideCompose([]byte(overrideFixture), "db", ComposeOverride{
		Ports:       []string{"13306:3306"},
		BindTargets: map[string]string{"/etc/mysql/conf.d": "/data/blog/conf.d"},
	})
	if err != nil {
		t.Fatalf("OverrideCompose error: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "13306:3306") || strings.Contains(got, "- 3306:3306") {
		t.Errorf("ports were not replaced:\n%s", got)
	}
	if !strings.Contains(got, "/data/blog/conf.d:/etc/mysql/conf.d:ro") {
		t.Errorf("bind host path was not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "blog-data:/var/lib/mysql") {
		t.Errorf("named volume entry must be preserved:\n%s", got)
	}
	// The untouched sibling service keeps its own port.
	if !strings.Contains(got, "80:80") {
		t.Errorf("unrelated service was modified:\n%s", got)
	}
}

func TestOverrideComposeIsNoOpWithoutEdits(t *testing.T) {
	out, err := OverrideCompose([]byte(overrideFixture), "db", ComposeOverride{})
	if err != nil {
		t.Fatalf("OverrideCompose error: %v", err)
	}
	if string(out) != overrideFixture {
		t.Errorf("an empty override must return the document unchanged:\n%s", out)
	}
}

func TestOverrideComposeRejectsUnknownService(t *testing.T) {
	_, err := OverrideCompose([]byte(overrideFixture), "ghost", ComposeOverride{Ports: []string{"1:1"}})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error = %v, want a refusal that names the unknown service", err)
	}
}

func TestOverrideComposeRemovesPortsWhenNoneRemain(t *testing.T) {
	out, err := OverrideCompose([]byte(overrideFixture), "web", ComposeOverride{Ports: []string{}})
	if err != nil {
		t.Fatalf("OverrideCompose error: %v", err)
	}
	if strings.Contains(string(out), "80:80") {
		t.Errorf("an explicitly empty port list must clear the service ports:\n%s", out)
	}
}

func TestParsePublishedPorts(t *testing.T) {
	ports := ParsePublishedPorts("0.0.0.0:3306->3306/tcp, [::]:3306->3306/tcp, 127.0.0.1:53->53/udp")
	if len(ports) != 3 {
		t.Fatalf("ports = %+v, want three entries", ports)
	}
	if ports[0].HostPort != "3306" || ports[0].Protocol != "tcp" {
		t.Errorf("ports[0] = %+v, want 3306/tcp", ports[0])
	}
	if ports[2].HostPort != "53" || ports[2].Protocol != "udp" {
		t.Errorf("ports[2] = %+v, want 53/udp", ports[2])
	}
	if got := ParsePublishedPorts("80/tcp"); len(got) != 0 {
		t.Errorf("container-only ports should be skipped, got %+v", got)
	}
}
