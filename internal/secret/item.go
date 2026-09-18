package secret

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SSHKeyItemLabel is the label OpsPulse gives the concealed field that holds a
// managed server's private key.
const SSHKeyItemLabel = "private key"

// PasswordItemLabel is the 1Password built-in field label holding a password.
const PasswordItemLabel = "password"

// Field ids inside the 1Password item categories OpsPulse writes to.
const (
	// sshKeyPrivateFieldID is the built-in "private key" field of a real
	// 1Password SSH Key item. OpsPulse reads such items when a user points a
	// key_path at one, but never writes them - see sshKeyManagedFieldID.
	sshKeyPrivateFieldID = "private_key"

	// sshKeyManagedFieldID is the custom concealed field OpsPulse creates on a
	// Login item to hold a managed server's private key.
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
	// A concealed field on a Login item is the only CLI-writable home for a
	// private key, so the id deliberately differs from the built-in
	// private_key: SSHKeyRefWithFormat uses that difference to decide whether an
	// op:// reference wants the ssh-format=openssh parameter, which real SSHKEY
	// fields require and a concealed text field rejects.
	sshKeyManagedFieldID = "opspulse_private_key"

	loginUsernameFieldID = "username"
	loginPasswordFieldID = "password"

	// inventoryNoteFieldID is the built-in notes field of a Secure Note, and
	// the home of the servers.yaml backup.
	inventoryNoteFieldID = "notesPlain"
)

// InventoryItemCategory is the 1Password category of the inventory backup item.
//
// A Secure Note rather than a Login: the payload is a YAML document, not a
// credential, and the built-in notes field is the only CLI-writable home for
// free text. Unlike a real SSH Key item (see sshKeyManagedFieldID), a Secure
// Note round-trips byte for byte through the CLI - verified against op 2.34.1
// for both `op item create` and `op item edit`, including a value with no
// trailing newline, so the read-back comparison can be exact.
const InventoryItemCategory = "Secure Note"

// InventoryItemTitle is the deterministic title of the single item holding the
// servers.yaml backup.
//
// There is deliberately one shared item rather than one per machine: the backup
// is a union of every machine that has pushed, so a new machine can restore the
// whole inventory from a single place.
const InventoryItemTitle = "opspulse_inventory"

// SSHKeyItemTitle returns the deterministic 1Password item title used to store a
// managed server's SSH key.
func SSHKeyItemTitle(serverName string) string {
	return "opspulse_" + serverName + "_key"
}

// PasswordItemTitle returns the deterministic 1Password item title used to store
// a managed server's password.
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
// inventory backup item.
func BuildInventoryRef(vault string) string {
	return fmt.Sprintf("%s%s/%s/%s", Prefix1P, vault, InventoryItemTitle, inventoryNoteFieldID)
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

// FillSSHKeyItem writes a private key onto a Login item document.
//
// doc may be either a fresh template (`op item template get Login`) for a create,
// or the current item (`op item get <item> --format json`) for an update. Re-using
// the item's own document on updates matters: `op item edit` consumes a whole
// item, so uploading a bare template would drop every field the user added by
// hand along with the item's identity.
//
// The key lands in a custom concealed field (sshKeyManagedFieldID) instead of a
// real SSH Key item's private_key, because the CLI cannot write those at all -
// see that constant's comment. A fresh Login template carries no such field, so
// one is appended; on updates the existing field is overwritten in place.
func FillSSHKeyItem(doc []byte, title, privateKey string) ([]byte, error) {
	if strings.TrimSpace(privateKey) == "" {
		return nil, fmt.Errorf("refusing to write an empty private key into 1Password")
	}

	item, err := decodeItem(doc, "Login")
	if err != nil {
		return nil, err
	}
	prepareItem(item, title)

	fields, _ := item["fields"].([]any)
	if updated, found := setItemField(fields, sshKeyManagedFieldID, privateKey); found {
		item["fields"] = updated
		return encodeItem(item)
	}

	item["fields"] = append(fields, map[string]any{
		"id":    sshKeyManagedFieldID,
		"label": SSHKeyItemLabel,
		"type":  "CONCEALED",
		"value": privateKey,
	})
	return encodeItem(item)
}

// FillLoginItem writes a username and password onto a 1Password Login item
// document, ready to be piped into `op item create` or `op item edit`.
func FillLoginItem(doc []byte, title, username, password string) ([]byte, error) {
	if strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("refusing to write an empty password into 1Password")
	}

	item, err := decodeItem(doc, "Login")
	if err != nil {
		return nil, err
	}
	prepareItem(item, title)

	fields, _ := item["fields"].([]any)
	if strings.TrimSpace(username) != "" {
		fields, _ = setItemField(fields, loginUsernameFieldID, username)
	}
	fields, found := setItemField(fields, loginPasswordFieldID, password)
	if !found {
		return nil, fmt.Errorf("the 1Password Login item has no %q field, so the password cannot be stored", loginPasswordFieldID)
	}
	item["fields"] = fields

	return encodeItem(item)
}

// FillInventoryItem writes the servers.yaml backup onto a Secure Note document.
//
// doc may be either a fresh template (`op item template get "Secure Note"`) for a
// create, or the current item (`op item get <id> --format json`) for an update,
// matching FillSSHKeyItem.
//
// Unlike the key field, notesPlain is a built-in field of the Secure Note
// template, so a missing one means the document is not what the caller assumed.
// Appending it is deliberately not attempted: a field the template does not
// define is exactly the shape that gets silently dropped on write.
func FillInventoryItem(doc []byte, yamlText string) ([]byte, error) {
	if strings.TrimSpace(yamlText) == "" {
		return nil, fmt.Errorf("refusing to write an empty inventory into 1Password")
	}

	item, err := decodeItem(doc, InventoryItemCategory)
	if err != nil {
		return nil, err
	}
	prepareItem(item, InventoryItemTitle)

	fields, _ := item["fields"].([]any)
	fields, found := setItemField(fields, inventoryNoteFieldID, yamlText)
	if !found {
		return nil, fmt.Errorf("the 1Password %s item has no %q field, so the inventory cannot be stored", InventoryItemCategory, inventoryNoteFieldID)
	}
	item["fields"] = fields

	return encodeItem(item)
}

func decodeItem(doc []byte, category string) (map[string]any, error) {
	var item map[string]any
	if err := json.Unmarshal(doc, &item); err != nil {
		return nil, fmt.Errorf("parse 1Password %s item: %w", category, err)
	}
	return item, nil
}

// prepareItem sets the fields common to both documents and drops the vault
// placeholder: the vault is always chosen explicitly on the command line, so
// leaving one in the payload only creates a way for the two to disagree.
func prepareItem(item map[string]any, title string) {
	item["title"] = title
	delete(item, "vault")
}

// setItemField writes value onto the field with the given id. It reports whether
// the field was present: the documents are produced by the CLI, so a missing
// field means the document is not what the caller assumed, and silently
// appending one could produce an item 1Password rejects or ignores.
func setItemField(fields []any, id, value string) ([]any, bool) {
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if fieldID, _ := field["id"].(string); fieldID == id {
			field["value"] = value
			return fields, true
		}
	}
	return fields, false
}

func encodeItem(item map[string]any) ([]byte, error) {
	out, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode 1Password item: %w", err)
	}
	return out, nil
}
