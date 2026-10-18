package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func setupTestHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("convert host key: %v", err)
	}
	return key
}

func setupTestServer(name, host string) server.Server {
	return server.Server{Name: name, Host: host, Port: 22, User: "root"}
}

func setupTestDeps(t *testing.T, servers ...server.Server) setupDeps {
	t.Helper()
	store := server.NewStore(filepath.Join(t.TempDir(), "servers.yaml"))
	for _, srv := range servers {
		if err := store.Save(srv); err != nil {
			t.Fatalf("seed server %q: %v", srv.Name, err)
		}
	}
	return setupDeps{
		store:       store,
		knownHosts:  filepath.Join(t.TempDir(), "known_hosts"),
		interactive: false,
	}
}

func setupTestKey(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "setup-test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	return pem.EncodeToMemory(block), pub
}

func TestRunServerSetupReadOnlyReportsMissingKeys(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	keyBytes, _ := setupTestKey(t)
	keyPath := filepath.Join(home, ".ssh", "opspulse_web")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := setupTestServer("web", "192.0.2.10")
	srv.KeyPath = "~/.ssh/opspulse_web"
	deps := setupTestDeps(t, srv)
	hostKey := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{
			Type:        hostKey.Type(),
			Fingerprint: ssh.FingerprintSHA256(hostKey),
			Source:      "fixture ssh-keyscan 192.0.2.10",
			Key:         hostKey,
		}}, nil
	}

	var out bytes.Buffer
	if err := runServerSetup(context.Background(), strings.NewReader(""), &out, setupOptions{}, nil, deps); err != nil {
		t.Fatalf("runServerSetup: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"host key missing",
		hostKey.Type(),
		ssh.FingerprintSHA256(hostKey),
		"source: fixture ssh-keyscan 192.0.2.10",
		"managed private key present",
		"ops doctor -f all",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("setup report missing %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(deps.knownHosts); !os.IsNotExist(err) {
		t.Fatal("read-only setup must not create known_hosts")
	}
}

func TestRunServerSetupWriteConfigPreservesHandwrittenEntries(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	srv := setupTestServer("web", "192.0.2.10")
	deps := setupTestDeps(t, srv)
	hostKey := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(hostKey), Key: hostKey}}, nil
	}
	deps.writeConfig = func(path string, servers []server.Server) error {
		_, _, err := server.UpdateSSHConfigFile(path, servers)
		return err
	}
	configPath := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := `Include ~/.ssh/config.d/*.conf

Host handwritten
    HostName handwritten.example
    ProxyCommand ssh -W %h:%p bastion

Match host internal-* exec "test -f ~/.ssh/internal"
    User deploy

Host *
    ServerAliveInterval 30
`
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := setupOptions{writeConfig: true, configPath: configPath}
	var first bytes.Buffer
	if err := runServerSetup(context.Background(), strings.NewReader(""), &first, opts, nil, deps); err != nil {
		t.Fatalf("first setup: %v", err)
	}
	firstContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Include ~/.ssh/config.d/*.conf",
		"Host handwritten",
		"ProxyCommand ssh -W %h:%p bastion",
		"Match host internal-*",
		"Host *",
		server.MarkerBegin,
		"Host web",
		server.MarkerEnd,
	} {
		if !strings.Contains(string(firstContent), want) {
			t.Errorf("managed config lost %q:\n%s", want, firstContent)
		}
	}

	var second bytes.Buffer
	if err := runServerSetup(context.Background(), strings.NewReader(""), &second, opts, nil, deps); err != nil {
		t.Fatalf("second setup: %v", err)
	}
	secondContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstContent) != string(secondContent) {
		t.Errorf("setup config write is not idempotent:\nfirst:\n%s\nsecond:\n%s", firstContent, secondContent)
	}
}

func TestRunServerSetupWriteConfigKeepsUnselectedManagedHosts(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	web := setupTestServer("web", "192.0.2.10")
	db := setupTestServer("db", "192.0.2.20")
	deps := setupTestDeps(t, web, db)
	hostKey := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(hostKey), Key: hostKey}}, nil
	}
	var written []string
	deps.writeConfig = func(_ string, servers []server.Server) error {
		for _, srv := range servers {
			written = append(written, srv.Name)
		}
		return nil
	}

	configPath := filepath.Join(home, ".ssh", "config")
	if err := runServerSetup(context.Background(), strings.NewReader(""), &bytes.Buffer{}, setupOptions{
		writeConfig: true,
		configPath:  configPath,
	}, []string{"web"}, deps); err != nil {
		t.Fatalf("runServerSetup: %v", err)
	}
	if strings.Join(written, ",") != "web,db" {
		t.Fatalf("managed config hosts = %v, want the complete inventory web,db", written)
	}
}

func TestRunServerSetupRefusesTrustWithoutConfirmation(t *testing.T) {
	srv := setupTestServer("web", "192.0.2.10")
	deps := setupTestDeps(t, srv)
	hostKey := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(hostKey), Key: hostKey}}, nil
	}

	err := runServerSetup(context.Background(), strings.NewReader(""), &bytes.Buffer{}, setupOptions{trustAll: true}, nil, deps)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("trust-all without --yes must fail closed, got %v", err)
	}
	if _, statErr := os.Stat(deps.knownHosts); !os.IsNotExist(statErr) {
		t.Fatal("refused trust must not create known_hosts")
	}
}

func TestRunServerSetupTrustOnlySelectedServerAfterConfirmation(t *testing.T) {
	web := setupTestServer("web", "192.0.2.10")
	db := setupTestServer("db", "192.0.2.20")
	deps := setupTestDeps(t, web, db)
	keys := map[string]ssh.PublicKey{
		"web": setupTestHostKey(t),
		"db":  setupTestHostKey(t),
	}
	deps.probe = func(_ context.Context, srv server.Server) ([]server.HostKey, error) {
		key := keys[srv.Name]
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}}, nil
	}
	deps.interactive = true

	var out bytes.Buffer
	err := runServerSetup(context.Background(), strings.NewReader("y\n"), &out, setupOptions{trust: []string{"web"}}, nil, deps)
	if err != nil {
		t.Fatalf("setup trust web: %v", err)
	}
	data, err := os.ReadFile(deps.knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "192.0.2.10") {
		t.Errorf("selected server key missing:\n%s", content)
	}
	if strings.Contains(content, "192.0.2.20") {
		t.Errorf("unselected server key was written:\n%s", content)
	}
	if !strings.Contains(out.String(), "Import 1 host key(s)") {
		t.Errorf("confirmation prompt was not shown:\n%s", out.String())
	}
}

func TestRunServerSetupTrustAllNonInteractiveNeedsYes(t *testing.T) {
	srv := setupTestServer("web", "192.0.2.10")
	deps := setupTestDeps(t, srv)
	key := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}}, nil
	}
	err := runServerSetup(context.Background(), strings.NewReader(""), &bytes.Buffer{}, setupOptions{trustAll: true}, nil, deps)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("non-interactive trust-all must require --yes, got %v", err)
	}
}

func TestRunServerSetupTrustAllWithYesImportsOnlyMissingHosts(t *testing.T) {
	web := setupTestServer("web", "192.0.2.10")
	db := setupTestServer("db", "192.0.2.20")
	deps := setupTestDeps(t, web, db)
	webKey := setupTestHostKey(t)
	dbKey := setupTestHostKey(t)
	if err := os.WriteFile(deps.knownHosts, []byte(knownhosts.Line([]string{net.JoinHostPort(web.Host, "22")}, webKey)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.probe = func(_ context.Context, srv server.Server) ([]server.HostKey, error) {
		key := dbKey
		if srv.Name == "web" {
			key = webKey
		}
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}}, nil
	}

	if err := runServerSetup(context.Background(), strings.NewReader(""), &bytes.Buffer{}, setupOptions{trustAll: true, yes: true}, nil, deps); err != nil {
		t.Fatalf("trust-all --yes: %v", err)
	}
	data, err := os.ReadFile(deps.knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Count(content, web.Host) != 1 {
		t.Errorf("the already-recorded host must not be appended again:\n%s", content)
	}
	if !strings.Contains(content, db.Host) {
		t.Errorf("the missing host was not imported:\n%s", content)
	}
}

func TestRunServerSetupRefusesMismatchedKnownHost(t *testing.T) {
	srv := setupTestServer("web", "192.0.2.10")
	deps := setupTestDeps(t, srv)
	existing := setupTestHostKey(t)
	incoming := setupTestHostKey(t)
	if err := os.WriteFile(deps.knownHosts, []byte(knownhosts.Line([]string{net.JoinHostPort(srv.Host, "22")}, existing)), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(incoming), Key: incoming}}, nil
	}

	var out bytes.Buffer
	err := runServerSetup(context.Background(), strings.NewReader(""), &out, setupOptions{trust: []string{"web"}, yes: true}, nil, deps)
	if err == nil || !strings.Contains(err.Error(), "different key") {
		t.Fatalf("mismatched host key must be refused, got %v", err)
	}
	data, readErr := os.ReadFile(deps.knownHosts)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(data)) != knownhosts.Line([]string{net.JoinHostPort(srv.Host, "22")}, existing) {
		t.Fatalf("known_hosts changed after refusal:\n%s", data)
	}
}

func TestRunServerSetupRestoreSeam(t *testing.T) {
	srv := setupTestServer("web", "192.0.2.10")
	deps := setupTestDeps(t, srv)
	hostKey := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(hostKey), Key: hostKey}}, nil
	}
	called := false
	deps.restore = func(context.Context, bool) error {
		called = true
		return nil
	}
	if err := runServerSetup(context.Background(), strings.NewReader(""), &bytes.Buffer{}, setupOptions{restore: true}, nil, deps); err != nil {
		t.Fatalf("runServerSetup restore: %v", err)
	}
	if !called {
		t.Fatal("--restore did not call the injected restore path")
	}
}

func TestRunServerSetupRestorePassesYesToExistingRestore(t *testing.T) {
	srv := setupTestServer("web", "192.0.2.10")
	deps := setupTestDeps(t, srv)
	hostKey := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(hostKey), Key: hostKey}}, nil
	}
	var gotYes bool
	deps.restore = func(_ context.Context, yes bool) error {
		gotYes = yes
		return nil
	}
	if err := runServerSetup(context.Background(), strings.NewReader(""), &bytes.Buffer{}, setupOptions{restore: true, yes: true}, nil, deps); err != nil {
		t.Fatalf("runServerSetup restore --yes: %v", err)
	}
	if !gotYes {
		t.Fatal("--yes was not passed to the existing 1Password restore path")
	}
}

func TestRunServerSetupConnectivityFailure(t *testing.T) {
	web := setupTestServer("web", "192.0.2.10")
	db := setupTestServer("db", "192.0.2.20")
	deps := setupTestDeps(t, web, db)
	hostKey := setupTestHostKey(t)
	deps.probe = func(context.Context, server.Server) ([]server.HostKey, error) {
		return []server.HostKey{{Fingerprint: ssh.FingerprintSHA256(hostKey), Key: hostKey}}, nil
	}
	var tested []string
	deps.testServer = func(_ context.Context, srv server.Server) error {
		tested = append(tested, srv.Name)
		if srv.Name == "db" {
			return errors.New("connection refused")
		}
		return nil
	}
	var out bytes.Buffer
	err := runServerSetup(context.Background(), strings.NewReader(""), &out, setupOptions{connect: true}, nil, deps)
	if err == nil || !strings.Contains(err.Error(), "failed the connectivity check") {
		t.Fatalf("connectivity failure must fail setup, got %v", err)
	}
	if strings.Join(tested, ",") != "web,db" {
		t.Errorf("tested servers = %v, want web,db", tested)
	}
	if !strings.Contains(out.String(), "❌ db: connection refused") {
		t.Errorf("connectivity failure was not reported:\n%s", out.String())
	}
}

func TestRunServerSetupWSLBoundary(t *testing.T) {
	srv := setupTestServer("web", "192.0.2.10")
	deps := setupTestDeps(t, srv)
	deps.wsl = true
	deps.opBinary = "C:\\Program Files\\1Password\\op.exe"
	deps.interactive = false
	var out bytes.Buffer
	if err := runServerSetup(context.Background(), strings.NewReader(""), &out, setupOptions{skipProbe: true}, nil, deps); err != nil {
		t.Fatalf("runServerSetup: %v", err)
	}
	got := out.String()
	for _, want := range []string{"WSL boundary", "op.exe", "~/.ssh", ".ssh/opspulse"} {
		if !strings.Contains(got, want) {
			t.Errorf("WSL boundary report missing %q:\n%s", want, got)
		}
	}
}

func TestServerSetupFlagsRegistered(t *testing.T) {
	for _, name := range []string{"restore", "write-config", "config", "trust", "trust-all", "connect", "yes", "skip-probe"} {
		if serverSetupCmd.Flags().Lookup(name) == nil {
			t.Errorf("ops server setup should expose --%s", name)
		}
	}
}

func TestManagedKeyPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX key permissions")
	}
	home := t.TempDir()
	setTestHome(t, home)
	keyPath := filepath.Join(home, ".ssh", "opspulse_web")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	keyBytes, _ := setupTestKey(t)
	if err := os.WriteFile(keyPath, keyBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if missing := printManagedKeyReport(&out, []server.Server{{Name: "web", KeyPath: "~/.ssh/opspulse_web"}}); missing != 0 {
		t.Fatalf("missing key count = %d, want 0", missing)
	}
	if !strings.Contains(out.String(), "want 0600") {
		t.Fatalf("loose key permissions were not reported:\n%s", out.String())
	}
}

func TestManagedKeyMissingIsCounted(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	var out bytes.Buffer
	missing := printManagedKeyReport(&out, []server.Server{{Name: "web", KeyPath: "~/.ssh/opspulse_web"}})
	if missing != 1 {
		t.Fatalf("missing key count = %d, want 1", missing)
	}
	if !strings.Contains(out.String(), "managed private key missing") {
		t.Fatalf("missing key was not reported:\n%s", out.String())
	}
}
