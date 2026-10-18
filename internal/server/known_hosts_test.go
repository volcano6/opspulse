package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func scannedLine(host string, key ssh.PublicKey) string {
	return host + " " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

func TestParseScannedHostKeys(t *testing.T) {
	first := testHostKey(t)
	second := testHostKey(t)
	output := scannedLine("example.test", first) + "\n" +
		scannedLine("example.test", first) + "\n" +
		scannedLine("example.test", second) + "\n" +
		"# comment\n"

	keys, err := ParseScannedHostKeys(output, "fixture")
	if err != nil {
		t.Fatalf("ParseScannedHostKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2 de-duplicated keys", len(keys))
	}
	if keys[0].Fingerprint != ssh.FingerprintSHA256(first) {
		t.Errorf("fingerprint = %q, want %q", keys[0].Fingerprint, ssh.FingerprintSHA256(first))
	}
	if keys[0].Type != first.Type() || keys[0].Source != "fixture" {
		t.Errorf("unexpected key metadata: %+v", keys[0])
	}
}

func TestParseScannedHostKeysRejectsEmptyOutput(t *testing.T) {
	if _, err := ParseScannedHostKeys("# nothing\n", "fixture"); err == nil {
		t.Fatal("expected an error for output with no usable key")
	}
}

func TestCheckKnownHostReportsPresentMissingAndMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	key := testHostKey(t)
	other := testHostKey(t)
	srv := Server{Name: "web", Host: "example.test", Port: 2222}
	address := net.JoinHostPort(srv.Host, "2222")

	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{address}, key)), 0o600); err != nil {
		t.Fatal(err)
	}

	present := CheckKnownHost(srv, path, []HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}})
	if present.Status != KnownHostPresent {
		t.Errorf("present status = %q, want %q (error %q)", present.Status, KnownHostPresent, present.Error)
	}
	if len(present.MatchedKeys) != 1 {
		t.Errorf("matched keys = %v, want one", present.MatchedKeys)
	}

	// A different key for an already recorded host is a mismatch, not "missing".
	// The missing case below uses a host the file has never seen.
	changed := CheckKnownHost(srv, path, []HostKey{{Fingerprint: ssh.FingerprintSHA256(other), Key: other}})
	if changed.Status != KnownHostMismatch {
		t.Errorf("changed status = %q, want %q (error %q)", changed.Status, KnownHostMismatch, changed.Error)
	}

	otherServer := Server{Name: "new", Host: "new.example.test", Port: 22}
	missing := CheckKnownHost(otherServer, path, []HostKey{{Fingerprint: ssh.FingerprintSHA256(other), Key: other}})
	if missing.Status != KnownHostMissing {
		t.Errorf("missing status = %q, want %q (error %q)", missing.Status, KnownHostMissing, missing.Error)
	}

	// The same host with a different key is a mismatch, never an invitation to
	// append. This is the case a backup of known_hosts could otherwise hide.
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{address}, other)), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatch := CheckKnownHost(srv, path, []HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}})
	if mismatch.Status != KnownHostMismatch {
		t.Errorf("mismatch status = %q, want %q", mismatch.Status, KnownHostMismatch)
	}
}

func TestCheckKnownHostAllowsAdditionalAlgorithm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	ed25519Key := testHostKey(t)
	rsaKey := testRSAHostKey(t)
	srv := Server{Name: "web", Host: "example.test"}
	address := net.JoinHostPort(srv.Host, "22")
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{address}, ed25519Key)), 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckKnownHost(srv, path, []HostKey{
		{Fingerprint: ssh.FingerprintSHA256(ed25519Key), Key: ed25519Key},
		{Fingerprint: ssh.FingerprintSHA256(rsaKey), Key: rsaKey},
	})
	if check.Status != KnownHostPresent {
		t.Fatalf("status = %q, want %q (error %q)", check.Status, KnownHostPresent, check.Error)
	}

	// Importing an additional algorithm is safe and should append exactly that
	// key, while the already-recorded key remains a no-op.
	n, err := AppendKnownHosts(path, srv, []HostKey{
		{Fingerprint: ssh.FingerprintSHA256(ed25519Key), Key: ed25519Key},
		{Fingerprint: ssh.FingerprintSHA256(rsaKey), Key: rsaKey},
	}, []string{ssh.FingerprintSHA256(ed25519Key), ssh.FingerprintSHA256(rsaKey)})
	if err != nil {
		t.Fatalf("AppendKnownHosts: %v", err)
	}
	if n != 1 {
		t.Errorf("appended %d keys, want 1", n)
	}
}

func TestCheckKnownHostMissingFileIsUnknownNotBroken(t *testing.T) {
	key := testHostKey(t)
	check := CheckKnownHost(Server{Name: "web", Host: "example.test"}, filepath.Join(t.TempDir(), "known_hosts"), []HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}})
	if check.Status != KnownHostMissing {
		t.Fatalf("status = %q, want %q", check.Status, KnownHostMissing)
	}
}

func testRSAHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(&private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAppendKnownHostsRequiresExplicitSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testHostKey(t)
	srv := Server{Name: "web", Host: "example.test"}
	if _, err := AppendKnownHosts(path, srv, []HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}}, nil); err == nil {
		t.Fatal("AppendKnownHosts must refuse an empty selection")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a refused import must not create known_hosts")
	}
}

func TestAppendKnownHostsWritesOnlySelectedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testHostKey(t)
	other := testHostKey(t)
	srv := Server{Name: "web", Host: "example.test", Port: 2222}
	keys := []HostKey{
		{Fingerprint: ssh.FingerprintSHA256(key), Key: key},
		{Fingerprint: ssh.FingerprintSHA256(other), Key: other},
	}

	n, err := AppendKnownHosts(path, srv, keys, []string{ssh.FingerprintSHA256(key)})
	if err != nil {
		t.Fatalf("AppendKnownHosts: %v", err)
	}
	if n != 1 {
		t.Errorf("appended %d keys, want 1", n)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ssh.FingerprintSHA256(key)) && !strings.Contains(string(data), key.Type()) {
		t.Errorf("known_hosts does not contain the selected key:\n%s", data)
	}
	if strings.Contains(string(data), ssh.FingerprintSHA256(other)) {
		t.Errorf("known_hosts contains an unselected key:\n%s", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("known_hosts mode = %o, want 600", info.Mode().Perm())
		}
	}

	// A second import of the same key is idempotent.
	n, err = AppendKnownHosts(path, srv, keys, []string{ssh.FingerprintSHA256(key)})
	if err != nil {
		t.Fatalf("second AppendKnownHosts: %v", err)
	}
	if n != 0 {
		t.Errorf("second import appended %d keys, want 0", n)
	}
}

func TestAppendKnownHostsRefusesMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	existing := testHostKey(t)
	incoming := testHostKey(t)
	srv := Server{Name: "web", Host: "example.test"}
	if err := os.WriteFile(path, []byte(knownhosts.Line([]string{net.JoinHostPort(srv.Host, "22")}, existing)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := AppendKnownHosts(path, srv, []HostKey{{Fingerprint: ssh.FingerprintSHA256(incoming), Key: incoming}}, []string{ssh.FingerprintSHA256(incoming)})
	if err == nil {
		t.Fatal("AppendKnownHosts must refuse a conflicting host key")
	}
	if !strings.Contains(err.Error(), "different key") {
		t.Errorf("error should explain the conflict: %v", err)
	}
}

func TestAppendKnownHostsRefusesMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	original := []byte("this is not a known_hosts entry\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	key := testHostKey(t)
	srv := Server{Name: "web", Host: "example.test"}
	_, err := AppendKnownHosts(path, srv, []HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}}, []string{ssh.FingerprintSHA256(key)})
	if err == nil {
		t.Fatal("AppendKnownHosts must refuse to edit a malformed known_hosts")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(original) {
		t.Fatalf("malformed known_hosts changed after refusal:\n%s", after)
	}
}

func TestCheckKnownHostReportsMalformedFileAsProbeFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("this is not a known_hosts entry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := testHostKey(t)
	check := CheckKnownHost(Server{Name: "web", Host: "example.test"}, path, []HostKey{{Fingerprint: ssh.FingerprintSHA256(key), Key: key}})
	if check.Status != KnownHostProbeFail {
		t.Fatalf("status = %q, want %q", check.Status, KnownHostProbeFail)
	}
	if check.Error == "" {
		t.Fatal("malformed known_hosts should explain the failure")
	}
}
