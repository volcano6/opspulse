package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/volcano6/opspulse/internal/secret"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

// Writing credentials back onto this machine: the private key of one server
// (including the public-key comparison that decides whether overwriting the file
// on disk is safe) and its plaintext password.

// restoreOneServer brings back every credential a single server keeps in
// 1Password, writing it to local disk.
//
// Key and password are checked independently on purpose: a server can hold
// both, and restoring only one of them would strand an op:// reference once the
// 1Password account is gone.
func restoreOneServer(ctx context.Context, store *server.Store, plan restorePlan) restoreOutcome {
	srv := plan.server
	out := restoreOutcome{name: srv.Name}

	if plan.keyRef == "" && len(plan.keyFromBlob) == 0 && plan.passRef == "" && plan.passFromBlob == "" {
		out.reason = "no credential is backed up in 1Password"
		return out
	}

	if plan.keyRef != "" || len(plan.keyFromBlob) > 0 {
		var (
			written bool
			err     error
		)
		if len(plan.keyFromBlob) > 0 {
			written, err = restoreKeyFromBlob(store, srv, plan.keyFromBlob)
		} else {
			written, err = restoreKeyFromOnePassword(ctx, store, srv, plan.keyRef)
		}
		switch {
		case err != nil:
			out.err = err
			return out
		case !written:
			out.blocked = true
			out.reason = "a different key already occupies the local path; re-run with --force to replace it"
		default:
			out.keyRestored = true
			out.migrated = out.migrated || plan.keyWasLegacy
		}
	}
	if plan.passRef != "" || plan.passFromBlob != "" {
		var err error
		if plan.passFromBlob != "" {
			err = restorePasswordFromBlob(store, srv, plan.passFromBlob)
		} else {
			err = restorePasswordFromOnePassword(ctx, store, srv, plan.passRef)
		}
		if err != nil {
			out.err = err
			return out
		}
		out.passRestored = true
		out.migrated = out.migrated || plan.passWasLegacy
	}
	return out
}

// restoreKeyFromOnePassword writes a 1Password-hosted private key onto local
// disk and rebinds the server to the file, leaving the item in 1Password alone.
//
// It reports whether the file was written: false with a nil error means the
// destination already holds a different key and was deliberately left alone.
func restoreKeyFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server, ref string) (bool, error) {
	vault, item, _, _ := secret.Parse1PRef(ref)
	fmt.Printf("⬇️  Restoring key for %q from 1Password (%s/%s)...\n", srv.Name, vault, item)

	keyData, err := onePasswordResolver().ResolveSSHKey(ctx, ref)
	if err != nil {
		return false, err
	}
	return writeRestoredKey(store, srv, []byte(keyData), fmt.Sprintf("the 1Password item %q", item))
}

// restoreKeyFromBlob writes the private key carried by this machine's backup
// document. No op call is involved: the key was read with the document.
func restoreKeyFromBlob(store *server.Store, srv *server.Server, keyData []byte) (bool, error) {
	fmt.Printf("⬇️  Restoring key for %q from the 1Password backup document...\n", srv.Name)
	return writeRestoredKey(store, srv, keyData, "the 1Password backup document")
}

// writeRestoredKey puts key material on local disk and rebinds the server to it.
//
// source names where the key came from for the error message, so that a
// corrupt item and a corrupt backup document are told apart.
func writeRestoredKey(store *server.Store, srv *server.Server, keyData []byte, source string) (bool, error) {
	if err := validatePrivateKeyContent(keyData); err != nil {
		return false, fmt.Errorf("%s does not contain a usable SSH private key: %w", source, err)
	}

	storedDest, expandedDest, err := setupKeyPath(srv.Name)
	if err != nil {
		return false, err
	}

	body := keyData
	if !strings.HasSuffix(string(keyData), "\n") {
		body = append(append([]byte(nil), keyData...), '\n')
	}

	replace, err := mayReplaceLocalKey(expandedDest, body)
	if err != nil {
		return false, err
	}
	if !replace {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(expandedDest), 0o700); err != nil {
		return false, fmt.Errorf("create ~/.ssh directory: %w", err)
	}
	if err := os.WriteFile(expandedDest, body, 0o600); err != nil {
		return false, fmt.Errorf("write private key to %s: %w", expandedDest, err)
	}
	writePublicKeyFile(expandedDest, body)

	if srv.KeyPath != storedDest {
		srv.KeyPath = storedDest
		if err := store.Save(*srv); err != nil {
			return false, fmt.Errorf("rebind server %q to the local key: %w", srv.Name, err)
		}
	}

	fmt.Printf("✅ %q: key restored to %s.\n", srv.Name, storedDest)
	return true, nil
}

// mayReplaceLocalKey reports whether an incoming key may overwrite whatever
// already sits at path.
//
// The comparison is by public key rather than by bytes. 1Password normalises a
// key on the way back out - an RSA key stored as classic PEM returns in OpenSSH
// format - so a byte comparison would report a conflict for a key that is in
// fact identical, and send the user to --force for no reason.
func mayReplaceLocalKey(path string, incoming []byte) (bool, error) {
	if onePasswordRestoreForce {
		return true, nil
	}
	existing, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- OpsPulse's own managed key location
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", path, err)
	}

	existingPub, existingOK := publicKeyLine(existing)
	incomingPub, incomingOK := publicKeyLine(incoming)
	if existingOK && incomingOK {
		return existingPub == incomingPub, nil
	}
	// One side is unparsable, so fall back to a byte comparison: that can only
	// over-report a conflict, never silently overwrite something unrecognised.
	return bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(incoming)), nil
}

// publicKeyLine derives the OpenSSH authorized_keys line of a private key,
// reporting false when the material cannot be parsed.
func publicKeyLine(keyData []byte) (string, bool) {
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), true
}

// restorePasswordFromOnePassword writes a 1Password-hosted password back into
// servers.yaml as plaintext, leaving the item in 1Password alone.
func restorePasswordFromOnePassword(ctx context.Context, store *server.Store, srv *server.Server, ref string) error {
	vault, item, _, _ := secret.Parse1PRef(ref)
	fmt.Printf("⬇️  Restoring password for %q from 1Password (%s/%s)...\n", srv.Name, vault, item)

	password, err := onePasswordResolver().ResolvePassword(ctx, ref)
	if err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("the 1Password item %q returned an empty password", item)
	}

	srv.Password = password
	if err := store.Save(*srv); err != nil {
		return fmt.Errorf("write the password back into servers.yaml: %w", err)
	}

	fmt.Printf("✅ %q: password restored into servers.yaml as plaintext.\n", srv.Name)
	return nil
}

// restorePasswordFromBlob writes a password carried by this machine's backup
// document back into servers.yaml as plaintext. No op call is involved.
func restorePasswordFromBlob(store *server.Store, srv *server.Server, password string) error {
	fmt.Printf("⬇️  Restoring password for %q from the 1Password backup document...\n", srv.Name)

	srv.Password = password
	if err := store.Save(*srv); err != nil {
		return fmt.Errorf("write the password back into servers.yaml: %w", err)
	}

	fmt.Printf("✅ %q: password restored into servers.yaml as plaintext.\n", srv.Name)
	return nil
}

func writePublicKeyFile(privateKeyPath string, keyData []byte) {
	signer, err := ssh.ParsePrivateKey(keyData)
	if err != nil {
		return
	}
	line := ssh.MarshalAuthorizedKey(signer.PublicKey())
	_ = os.WriteFile(privateKeyPath+".pub", line, 0o600)
}
