package secret

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SSHKeyItemLabel is the 1Password built-in field label holding an SSH key's
// private half.
const SSHKeyItemLabel = "private key"

// PasswordItemLabel is the 1Password built-in field label holding a password.
const PasswordItemLabel = "password"

// Field ids inside the 1Password item categories OpsPulse writes to. They come
// from `op item template get "SSH Key"` and `op item template get Login`, and
// are what the item documents are keyed by.
const (
	sshKeyPrivateFieldID = "private_key"
	sshKeyPublicFieldID  = "public_key"
	loginUsernameFieldID = "username"
	loginPasswordFieldID = "password"
)

// SSHKeyItemTitle returns the deterministic 1Password item title used to store a
// managed server's SSH key.
func SSHKeyItemTitle(serverName string) string {
	return "opspulse_" + serverName
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

// BuildSSHKeyRef builds the op:// reference pointing at the private key field of
// a managed SSH key item.
func BuildSSHKeyRef(vault, title string) string {
	return fmt.Sprintf("%s%s/%s/%s", Prefix1P, vault, title, SSHKeyItemLabel)
}

// BuildPasswordRef builds the op:// reference pointing at the password field of
// a managed Login item.
func BuildPasswordRef(vault, title string) string {
	return fmt.Sprintf("%s%s/%s/%s", Prefix1P, vault, title, PasswordItemLabel)
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

// FillSSHKeyItem writes an SSH key onto a 1Password item document.
//
// doc may be either a fresh template (`op item template get "SSH Key"`) for a
// create, or the current item (`op item get <item> --format json`) for an
// update. Re-using the item's own document on updates matters: `op item edit`
// consumes a whole item, so uploading a bare template would drop every field the
// user added by hand along with the item's identity.
//
// The JSON indirection is mandatory rather than stylistic. 1Password assignment
// statements (the `field=value` command line form) do not support the SSHKEY
// field type at all, so an existing private key can only be imported through an
// item document; passing it as a command line argument would also expose it to
// every process on the machine via the process list.
func FillSSHKeyItem(doc []byte, title, privateKey, publicKey string) ([]byte, error) {
	if strings.TrimSpace(privateKey) == "" {
		return nil, fmt.Errorf("refusing to write an empty private key into 1Password")
	}

	item, err := decodeItem(doc, "SSH Key")
	if err != nil {
		return nil, err
	}
	prepareItem(item, title)

	fields, _ := item["fields"].([]any)
	fields, found := setItemField(fields, sshKeyPrivateFieldID, privateKey)
	if !found {
		return nil, fmt.Errorf("the 1Password SSH Key item has no %q field, so the key cannot be stored", sshKeyPrivateFieldID)
	}
	// 1Password derives the public key from the private key, and the stock
	// template carries no field for it. Only fill one when the document
	// happens to have it; never invent a custom field.
	if strings.TrimSpace(publicKey) != "" {
		fields, _ = setItemField(fields, sshKeyPublicFieldID, publicKey)
	}
	item["fields"] = fields

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
