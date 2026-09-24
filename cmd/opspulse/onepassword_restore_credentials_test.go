package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testPrivateKeyPEM builds a deterministic OpenSSH-format ed25519 private key.
// Generating it here keeps key material out of the repository and means the
// tests never read or write the developer's real ~/.ssh.
func testPrivateKeyPEM(t *testing.T, seed byte) []byte {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	block, err := ssh.MarshalPrivateKey(priv, "opspulse:test")
	if err != nil {
		t.Fatalf("marshal test key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

func TestWritePublicKeyFileIgnoresInvalidMaterial(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "junk")
	writePublicKeyFile(keyPath, []byte("definitely not a key"))

	if _, err := os.Stat(keyPath + ".pub"); !os.IsNotExist(err) {
		t.Error("no .pub file should be written for unparsable key material")
	}
}

func TestPublicKeyLine(t *testing.T) {
	line, ok := publicKeyLine(testPrivateKeyPEM(t, 3))
	if !ok {
		t.Fatal("publicKeyLine() should parse a generated key")
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") {
		t.Errorf("publicKeyLine() = %q, want an ssh-ed25519 authorized_keys line", line)
	}
	if _, ok := publicKeyLine([]byte("not a key")); ok {
		t.Error("publicKeyLine() should report failure for unparsable material")
	}
}

// TestMayReplaceLocalKey pins the guard that stops a batch restore from
// silently destroying a key file that already lives at the managed path.
func TestMayReplaceLocalKey(t *testing.T) {
	keyA := testPrivateKeyPEM(t, 1)
	keyB := testPrivateKeyPEM(t, 2)

	writeFixture := func(t *testing.T, data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "opspulse_web")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return path
	}

	assertDecision := func(t *testing.T, got bool, err error, want bool) {
		t.Helper()
		if err != nil {
			t.Fatalf("mayReplaceLocalKey() error = %v, want nil", err)
		}
		if got != want {
			t.Errorf("mayReplaceLocalKey() = %v, want %v", got, want)
		}
	}

	t.Run("a missing destination is free to write", func(t *testing.T) {
		got, err := mayReplaceLocalKey(filepath.Join(t.TempDir(), "absent"), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("an identical key is written without ceremony", func(t *testing.T) {
		got, err := mayReplaceLocalKey(writeFixture(t, keyA), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("the same key with different surrounding bytes is still the same key", func(t *testing.T) {
		padded := append([]byte("\n"), keyA...)
		padded = append(padded, '\n', '\n')
		got, err := mayReplaceLocalKey(writeFixture(t, padded), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("a different key is left alone", func(t *testing.T) {
		got, err := mayReplaceLocalKey(writeFixture(t, keyB), keyA)
		assertDecision(t, got, err, false)
	})

	t.Run("--force overrides the guard", func(t *testing.T) {
		onePasswordRestoreForce = true
		t.Cleanup(func() { onePasswordRestoreForce = false })

		got, err := mayReplaceLocalKey(writeFixture(t, keyB), keyA)
		assertDecision(t, got, err, true)
	})

	t.Run("unparsable content falls back to a byte comparison", func(t *testing.T) {
		path := writeFixture(t, []byte("not a key"))
		got, err := mayReplaceLocalKey(path, keyA)
		assertDecision(t, got, err, false)

		got, err = mayReplaceLocalKey(path, []byte("not a key"))
		assertDecision(t, got, err, true)
	})
}

// TestMayReplaceLocalKeyNormalisesKeyFormats is the reason the comparison is by
// public key rather than by bytes: 1Password hands a key back in OpenSSH form
// even when it was uploaded as classic PEM, so a byte comparison would demand
// --force for a key that never changed.
func TestMayReplaceLocalKeyNormalisesKeyFormats(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	classicPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
	})
	block, err := ssh.MarshalPrivateKey(rsaKey, "")
	if err != nil {
		t.Fatalf("marshal OpenSSH key: %v", err)
	}
	openSSHForm := pem.EncodeToMemory(block)

	if bytes.Equal(classicPEM, openSSHForm) {
		t.Fatal("fixture assumption broken: the two encodings should differ byte for byte")
	}

	path := filepath.Join(t.TempDir(), "rsa_key")
	if err := os.WriteFile(path, classicPEM, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := mayReplaceLocalKey(path, openSSHForm)
	if err != nil {
		t.Fatalf("mayReplaceLocalKey() error = %v, want nil", err)
	}
	if !got {
		t.Error("the same RSA key in a different encoding must not be reported as a conflict")
	}
}
