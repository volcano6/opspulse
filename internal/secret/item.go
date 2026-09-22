package secret

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PasswordItemLabel is the 1Password built-in field label holding a password.
const PasswordItemLabel = "password"

// Field ids inside the 1Password item categories OpsPulse reads from.
const (
	// sshKeyManagedFieldID is the custom concealed field OpsPulse used to create
	// on a Login item to hold a managed server's private key.
	//
	// It exists because the 1Password CLI cannot write SSH Key items at all.
	// Verified against op 2.34.1:
	//
	//   - `op item create` accepts a private_key value, echoes it back, exits 0,
	//     and then the item has no such field (both PEM and OpenSSH keys).
	//   - `op item edit` refuses outright: "SSH Key item editing in the CLI is
	//     not yet supported."
	//   - `op item get <id> --format json` fails on the resulting empty item.
	//
	// Nothing writes this field any more - credentials travel inside the
	// per-machine backup item now - but restore still reads items an older
	// OpsPulse created. The id deliberately differs from the built-in
	// private_key: SSHKeyRefWithFormat uses that difference to decide whether an
	// op:// reference wants the ssh-format=openssh parameter, which real SSHKEY
	// fields require and a concealed text field rejects.
	sshKeyManagedFieldID = "opspulse_private_key"

	// inventoryNoteFieldID is the built-in notes field of a Secure Note, and
	// the home of the backup payload.
	inventoryNoteFieldID = "notesPlain"
)

// InventoryItemCategory is the human-readable name of the 1Password category of
// the inventory backup item.
//
// A Secure Note rather than a Login: the payload is a document, not a
// credential, and the built-in notes field is the only CLI-writable home for
// free text. Unlike a real SSH Key item (see sshKeyManagedFieldID), a Secure
// Note round-trips byte for byte through the CLI - verified against op 2.39.0
// for both `op item create` and `op item edit`, including a value with no
// trailing newline, so the read-back comparison can be exact.
const InventoryItemCategory = "Secure Note"

// inventoryItemCategoryID is the category as the CLI's JSON documents spell it.
const inventoryItemCategoryID = "SECURE_NOTE"

// InventoryItemTitle is the title of the historical shared inventory item, and
// the prefix of every per-machine title.
//
// It is still read, as the fallback for a vault that only holds the old format,
// but it is no longer written: a shared item has to be merged on every backup,
// which means reading it first, which means the backup could not be a single
// write. See InventoryItemTitleFor.
const InventoryItemTitle = "opspulse_inventory"

// InventoryItemTitleFor returns the title of one machine's backup item.
//
// One item per machine rather than one shared item: the payload is the whole
// servers.yaml plus every private key this machine holds (~14KB), and 1Password
// stores a single field of that size without complaint. Keeping it in one item
// makes a backup one write plus one read-back check instead of a per-credential
// round trip through the Desktop App.
//
// machine must already be sanitised; see cmd/opspulse's machineName.
func InventoryItemTitleFor(machine string) string {
	return InventoryItemTitle + "_" + machine
}

// IsInventoryItemTitle reports whether a vault item title belongs to an
// inventory backup, of either the per-machine or the historical shared shape.
func IsInventoryItemTitle(title string) bool {
	return strings.HasPrefix(title, InventoryItemTitle)
}

// SSHKeyItemTitle returns the deterministic 1Password item title an older
// OpsPulse stored a managed server's SSH key under.
func SSHKeyItemTitle(serverName string) string {
	return "opspulse_" + serverName + "_key"
}

// PasswordItemTitle returns the deterministic 1Password item title an older
// OpsPulse stored a managed server's password under.
//
// It deliberately differs from SSHKeyItemTitle: a server that carries both a key
// and a password would otherwise have its credentials competing for one item,
// and a "Login" item replacing an "SSH Key" item would destroy the pushed key.
func PasswordItemTitle(serverName string) string {
	return "opspulse_" + serverName + "_password"
}

// BuildSSHKeyRef builds the op:// reference pointing at the concealed field of a
// managed SSH key item.
//
// No ssh-format query parameter is appended: the key is stored as plain text in
// a concealed field rather than as a 1Password SSHKEY field, and `op` rejects
// the parameter on anything but a real SSHKEY field.
func BuildSSHKeyRef(vault, title string) string {
	return fmt.Sprintf("%s%s/%s/%s", Prefix1P, vault, title, sshKeyManagedFieldID)
}

// BuildPasswordRef builds the op:// reference pointing at the password field of
// a managed Login item.
func BuildPasswordRef(vault, title string) string {
	return fmt.Sprintf("%s%s/%s/%s", Prefix1P, vault, title, PasswordItemLabel)
}

// BuildInventoryRef builds the op:// reference pointing at the body of the
// inventory backup item with the given title.
func BuildInventoryRef(vault, title string) string {
	return fmt.Sprintf("%s%s/%s/%s", Prefix1P, vault, title, inventoryNoteFieldID)
}

// Parse1PRef splits an op://<vault>/<item>/<field> reference. The field part is
// optional, and any query string (for example ssh-format=openssh) is stripped.
func Parse1PRef(ref string) (vault, item, field string, ok bool) {
	trimmed := strings.TrimSpace(ref)
	if !Is1PRef(trimmed) {
		return "", "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(trimmed, Prefix1P), "/", 3)
	if len(parts) < 2 {
		return "", "", "", false
	}
	vault, item = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if vault == "" || item == "" {
		return "", "", "", false
	}
	if len(parts) == 3 {
		field = strings.TrimSpace(parts[2])
		if idx := strings.Index(field, "?"); idx >= 0 {
			field = strings.TrimSpace(field[:idx])
		}
	}
	return vault, item, field, true
}

// ParseSSHKeyRef splits an op://<vault>/<item>/private key reference.
func ParseSSHKeyRef(ref string) (vault, item string, ok bool) {
	vault, item, _, ok = Parse1PRef(ref)
	return vault, item, ok
}

// BuildInventoryItem returns a Secure Note document holding payload, ready to be
// piped into `op item create -` or `op item edit <title>`.
//
// The document is built here rather than fetched with `op item template get`
// because every op call is a full round trip through the Desktop App (measured
// at 3-9s, uncached on Windows), and spending as few of them as possible is the
// entire point of the backup's design. The template's only contribution is an
// empty notesPlain field, which this reproduces exactly. Verified against op
// 2.39.0: create accepts it, and edit accepts it while updating the existing item
// in place rather than creating a second one.
//
// The trade-off is that a field the user added to the backup item by hand is not
// carried over on an update. The item is OpsPulse's own and the payload is
// machine-generated, so there is nothing of the user's to lose.
func BuildInventoryItem(title, payload string) ([]byte, error) {
	if strings.TrimSpace(payload) == "" {
		return nil, fmt.Errorf("refusing to write an empty inventory into 1Password")
	}
	return encodeItem(map[string]any{
		"title":    title,
		"category": inventoryItemCategoryID,
		"fields": []any{
			map[string]any{
				"id":      inventoryNoteFieldID,
				"type":    "STRING",
				"label":   inventoryNoteFieldID,
				"purpose": "NOTES",
				"value":   payload,
			},
		},
	})
}

func encodeItem(item map[string]any) ([]byte, error) {
	out, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode 1Password item: %w", err)
	}
	return out, nil
}
