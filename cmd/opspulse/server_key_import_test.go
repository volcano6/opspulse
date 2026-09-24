package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

func generateTestKey(t *testing.T) []byte {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	pemBlock, err := ssh.MarshalPrivateKey(priv, "opspulse-test")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return pem.EncodeToMemory(pemBlock)
}

func TestResolveAndSecureKeyPath_AlreadyInSSHDir(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}

	keyFile := filepath.Join(sshDir, "id_ed25519")
	if err := os.WriteFile(keyFile, generateTestKey(t), 0o644); err != nil { //nolint:gosec // G306: Testing permission tightening from 0644 to 0600
		t.Fatal(err)
	}

	var out bytes.Buffer
	got, copied, err := ResolveAndSecureKeyPath(nil, &out, "oracle-sg", "~/.ssh/id_ed25519", false)
	if err != nil {
		t.Fatalf("ResolveAndSecureKeyPath() error: %v", err)
	}
	if copied {
		t.Errorf("expected copied=false, got true")
	}
	if got != "~/.ssh/id_ed25519" {
		t.Errorf("got %q, want ~/.ssh/id_ed25519", got)
	}

	// Verify permissions were tightened to 0600
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 mode, got %o", info.Mode().Perm())
	}
}

func TestResolveAndSecureKeyPath_OutsideSSHDir_UserConfirms(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	downloadsDir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(downloadsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	downloadedKey := filepath.Join(downloadsDir, "oracle.pem")
	if err := os.WriteFile(downloadedKey, generateTestKey(t), 0o644); err != nil { //nolint:gosec // G306: Testing permission tightening from 0644 to 0600
		t.Fatal(err)
	}

	in := strings.NewReader("y\n")
	var out bytes.Buffer

	got, copied, err := ResolveAndSecureKeyPath(in, &out, "oracle-sg", downloadedKey, false)
	if err != nil {
		t.Fatalf("ResolveAndSecureKeyPath() error: %v", err)
	}
	if !copied {
		t.Errorf("expected copied=true, got false")
	}

	wantStored := "~/.ssh/opspulse_oracle-sg.pem"
	if got != wantStored {
		t.Errorf("got %q, want %q", got, wantStored)
	}

	// Verify destination file exists in ~/.ssh/
	destFile := filepath.Join(home, ".ssh", "opspulse_oracle-sg.pem")
	destInfo, err := os.Stat(destFile)
	if err != nil {
		t.Fatalf("destination key not found: %v", err)
	}
	if runtime.GOOS != "windows" && destInfo.Mode().Perm() != 0o600 {
		t.Errorf("expected destination mode 0600, got %o", destInfo.Mode().Perm())
	}
}

func TestResolveAndSecureKeyPath_OutsideSSHDir_UserDeclines(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	customDir := filepath.Join(home, "keys")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatal(err)
	}

	customKey := filepath.Join(customDir, "custom.key")
	if err := os.WriteFile(customKey, generateTestKey(t), 0o644); err != nil { //nolint:gosec // G306: Testing permission tightening from 0644 to 0600
		t.Fatal(err)
	}

	in := strings.NewReader("n\n")
	var out bytes.Buffer

	got, copied, err := ResolveAndSecureKeyPath(in, &out, "my-server", customKey, false)
	if err != nil {
		t.Fatalf("ResolveAndSecureKeyPath() error: %v", err)
	}
	if copied {
		t.Errorf("expected copied=false, got true")
	}

	if got != customKey {
		t.Errorf("got %q, want original path %q", got, customKey)
	}

	// Verify original file was chmod to 0600
	info, err := os.Stat(customKey)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("expected 0600 mode on original file, got %o", info.Mode().Perm())
	}
}

func TestResolveAndSecureKeyPath_NoCopyFlag(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	customDir := filepath.Join(home, "keys")
	_ = os.MkdirAll(customDir, 0o755)

	customKey := filepath.Join(customDir, "my.key")
	_ = os.WriteFile(customKey, generateTestKey(t), 0o644) //nolint:gosec // G306: Testing mock key permissions

	var out bytes.Buffer
	got, copied, err := ResolveAndSecureKeyPath(nil, &out, "server-a", customKey, true)
	if err != nil {
		t.Fatalf("ResolveAndSecureKeyPath() error: %v", err)
	}
	if copied {
		t.Errorf("expected copied=false, got true")
	}
	if got != customKey {
		t.Errorf("got %q, want %q", got, customKey)
	}
}

func TestResolveAndSecureKeyPath_InvalidKeyContent(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "invalid.txt")
	_ = os.WriteFile(tmpFile, []byte("this is just a text file, not a key"), 0o644) //nolint:gosec // G306: Testing invalid key handling

	_, _, err := ResolveAndSecureKeyPath(nil, nil, "srv", tmpFile, false)
	if err == nil {
		t.Error("expected error for invalid key content, got nil")
	}
}

func TestResolveAndSecureKeyPath_NonExistentKey(t *testing.T) {
	_, _, err := ResolveAndSecureKeyPath(nil, nil, "srv", "/non/existent/path/key.pem", false)
	if err == nil {
		t.Error("expected error for non-existent key, got nil")
	}
}

func TestCleanupManagedKey(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	sshDir := filepath.Join(home, ".ssh")
	_ = os.MkdirAll(sshDir, 0o700)

	managedKey := filepath.Join(sshDir, "opspulse_test_vps.pem")
	_ = os.WriteFile(managedKey, []byte("key"), 0o600)
	_ = os.WriteFile(managedKey+".pub", []byte("pub"), 0o600)

	unmanagedKey := filepath.Join(sshDir, "id_rsa")
	_ = os.WriteFile(unmanagedKey, []byte("rsa"), 0o600)

	// Clean up managed key
	cleanupManagedKey("~/.ssh/opspulse_test_vps.pem")

	if _, err := os.Stat(managedKey); !os.IsNotExist(err) {
		t.Errorf("expected managed key to be removed, but still exists")
	}
	if _, err := os.Stat(managedKey + ".pub"); !os.IsNotExist(err) {
		t.Errorf("expected managed pub key to be removed, but still exists")
	}

	// Clean up unmanaged key should do nothing
	cleanupManagedKey("~/.ssh/id_rsa")
	if _, err := os.Stat(unmanagedKey); err != nil {
		t.Errorf("unmanaged key should not be removed: %v", err)
	}
}

func TestIsKeyUsedByOtherServers(t *testing.T) {
	dir := t.TempDir()
	store := server.NewStore(filepath.Join(dir, "servers.yaml"))

	_ = store.Save(server.Server{Name: "vps1", Host: "1.1.1.1", KeyPath: "~/.ssh/opspulse_shared.key"})
	_ = store.Save(server.Server{Name: "vps2", Host: "2.2.2.2", KeyPath: "~/.ssh/opspulse_shared.key"})
	_ = store.Save(server.Server{Name: "vps3", Host: "3.3.3.3", KeyPath: "~/.ssh/id_ed25519"})

	// Checking from vps1 perspective: vps2 still uses shared.key
	used, other, err := IsKeyUsedByOtherServers(store, "vps1", "~/.ssh/opspulse_shared.key")
	if err != nil {
		t.Fatal(err)
	}
	if !used || other != "vps2" {
		t.Errorf("expected used=true, other=vps2, got used=%v, other=%q", used, other)
	}

	// Checking from vps3 perspective: no one else uses id_ed25519
	used, _, err = IsKeyUsedByOtherServers(store, "vps3", "~/.ssh/id_ed25519")
	if err != nil {
		t.Fatal(err)
	}
	if used {
		t.Errorf("expected used=false for vps3 key, got true")
	}
}

func TestCleanupManagedKeyWithRefCheck(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	sshDir := filepath.Join(home, ".ssh")
	_ = os.MkdirAll(sshDir, 0o700)

	sharedKey := filepath.Join(sshDir, "opspulse_shared.key")
	_ = os.WriteFile(sharedKey, []byte("key"), 0o600)

	singleKey := filepath.Join(sshDir, "opspulse_single.key")
	_ = os.WriteFile(singleKey, []byte("key"), 0o600)

	store := server.NewStore(filepath.Join(home, "servers.yaml"))
	_ = store.Save(server.Server{Name: "vps1", Host: "1.1.1.1", KeyPath: "~/.ssh/opspulse_shared.key"})
	_ = store.Save(server.Server{Name: "vps2", Host: "2.2.2.2", KeyPath: "~/.ssh/opspulse_shared.key"})
	_ = store.Save(server.Server{Name: "vps3", Host: "3.3.3.3", KeyPath: "~/.ssh/opspulse_single.key"})

	// Case 1: Shared key should NOT be deleted when vps1 is deleted
	var out bytes.Buffer
	err := CleanupManagedKeyWithRefCheck(&out, store, "vps1", "~/.ssh/opspulse_shared.key", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sharedKey); os.IsNotExist(err) {
		t.Errorf("shared key should not be deleted when still used by vps2")
	}

	// Case 2: keepKey is true -> single key is preserved
	err = CleanupManagedKeyWithRefCheck(&out, store, "vps3", "~/.ssh/opspulse_single.key", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(singleKey); os.IsNotExist(err) {
		t.Errorf("single key should be kept when keepKey=true")
	}

	// Case 3: Single key without other references is automatically cleaned up
	err = CleanupManagedKeyWithRefCheck(&out, store, "vps3", "~/.ssh/opspulse_single.key", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(singleKey); !os.IsNotExist(err) {
		t.Errorf("single key should be cleaned up when no other server references it")
	}
}

// isManagedKey decides which private keys OpsPulse may delete, so its boundary
// has to be measured per path component. The previous prefix-only test called
// a same-prefix sibling directory and a path escaping ~/.ssh managed keys.
func TestIsManagedKeyPathBoundary(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sshDir := filepath.Join(home, ".ssh")

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"same-prefix sibling directory", filepath.Join(sshDir, "opspulse_archive", "id_rsa"), false},
		{"traversal out of ~/.ssh", sshDir + "/opspulse_x/../../etc/foo", false},
		{"managed key", filepath.Join(sshDir, "opspulse_a"), true},
		{"same prefix outside ~/.ssh", filepath.Join(string(filepath.Separator), "tmp", "opspulse_a"), false},
		{"unmanaged key in ~/.ssh", filepath.Join(sshDir, "id_rsa"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isManagedKey(tt.path); got != tt.want {
				t.Errorf("isManagedKey(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCleanupManagedKeySparesPathsOutsideTheManagedNamespace(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	sshDir := filepath.Join(home, ".ssh")

	sibling := filepath.Join(sshDir, "opspulse_archive", "id_rsa")
	writeTestFile(t, sibling)
	// The traversal resolves to <home>/etc/foo. It goes through the sibling
	// directory created above, so it is a path the kernel can actually reach -
	// the old prefix check accepted it and deleted the file behind it.
	escaped := filepath.Join(home, "etc", "foo")
	writeTestFile(t, escaped)
	traversal := filepath.Join(sshDir, "opspulse_archive") + "/../../etc/foo"

	cleanupManagedKey(sibling)
	cleanupManagedKey(traversal)

	for _, path := range []string{sibling, escaped} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("cleanupManagedKey deleted %s, which OpsPulse does not manage: %v", path, err)
		}
	}
}
