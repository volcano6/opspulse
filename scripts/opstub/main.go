// Command opstub is a stand-in for the 1Password CLI. It exists so that the
// `ops 1p push` / `ops 1p pull` paths can be exercised end to end without a real
// 1Password account and without interactive authorisation: the real `op` needs a
// Desktop App approval that a non-interactive harness can never satisfy.
//
// It is a local verification fixture, not part of the shipped product, and it is
// created and deleted by the verification run.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const sshKeyTemplate = `{
  "title": "Imported Key",
  "category": "SSH_KEY",
  "fields": [
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "value": ""},
    {"id": "private_key", "type": "SSHKEY", "label": "private key", "value": ""}
  ]
}`

const loginTemplate = `{
  "title": "",
  "category": "LOGIN",
  "fields": [
    {"id": "username", "type": "STRING", "label": "username", "purpose": "USERNAME", "value": ""},
    {"id": "password", "type": "CONCEALED", "label": "password", "purpose": "PASSWORD", "value": ""},
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "purpose": "NOTES", "value": ""}
  ]
}`

// existingItem mirrors `op item get <id> --format json` for a managed key item:
// a Login item carrying OpsPulse's custom concealed field, which the user has
// since annotated.
const existingItem = `{
  "id": "item-opspulse_web_key",
  "title": "opspulse_web_key",
  "version": 7,
  "vault": {"id": "vault-uuid"},
  "category": "LOGIN",
  "tags": ["infra"],
  "fields": [
    {"id": "username", "type": "STRING", "label": "username", "purpose": "USERNAME", "value": ""},
    {"id": "password", "type": "CONCEALED", "label": "password", "purpose": "PASSWORD", "value": ""},
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "purpose": "NOTES", "value": "user note"},
    {"id": "opspulse_private_key", "type": "CONCEALED", "label": "private key", "value": "OLD"}
  ]
}`

func main() {
	args := os.Args[1:]
	stdin := readStdin()

	if path := os.Getenv("STUB_OP_LOG"); path != "" {
		logLine(path, args, stdin)
	}

	if len(args) == 0 {
		fatalf("stub: no arguments")
	}

	switch args[0] {
	case "vault":
		// vault list --format json
		vaults := strings.Split(envOr("STUB_OP_VAULTS", "Personal"), ",")
		items := make([]map[string]string, 0, len(vaults))
		for _, v := range vaults {
			items = append(items, map[string]string{"name": strings.TrimSpace(v)})
		}
		writeJSON(items)

	case "account":
		writeJSON([]map[string]string{{"url": "my.1password.com", "email": "volov@example.com"}})

	case "item":
		handleItem(args[1:], stdin)

	case "read":
		handleRead(args[1:])

	default:
		fatalf("stub: unsupported command %q", strings.Join(args, " "))
	}
}

func handleItem(args []string, stdin []byte) {
	if len(args) == 0 {
		fatalf("stub: item needs a subcommand")
	}

	switch args[0] {
	case "template":
		// item template get <category>
		if len(args) < 3 {
			fatalf("stub: item template get needs a category")
		}
		switch strings.ToLower(args[2]) {
		case "ssh key", "sshkey":
			fmt.Print(sshKeyTemplate)
		case "login":
			fmt.Print(loginTemplate)
		default:
			fatalf("stub: unknown template %q", args[2])
		}

	case "list":
		// item list --vault V --format json
		// STUB_OP_EXISTING is a comma-separated list of titles, so a single run
		// can exercise both the update path and --from-vault discovery.
		var items []map[string]string
		for _, title := range strings.Split(os.Getenv("STUB_OP_EXISTING"), ",") {
			if title = strings.TrimSpace(title); title == "" {
				continue
			}
			items = append(items, map[string]string{"id": "item-" + title, "title": title})
		}
		if items == nil {
			items = []map[string]string{}
		}
		writeJSON(items)

	case "get":
		// item get <id> --vault V --format json
		fmt.Print(existingItem)

	case "create":
		// item create --vault V -
		assertItemDocument(stdin, "create")
		writeJSON(map[string]string{"id": "item-created"})

	case "edit":
		// item edit <id> --vault V  (payload arrives on stdin)
		assertItemDocument(stdin, "edit")
		writeJSON(map[string]string{"id": "item-existing"})

	default:
		fatalf("stub: unsupported item subcommand %q", args[0])
	}
}

func handleRead(args []string) {
	if len(args) == 0 {
		fatalf("stub: read needs a reference")
	}
	ref := args[0]
	if strings.Contains(ref, "/password") {
		fmt.Print(envOr("STUB_OP_PASSWORD", "stub-vault-pw"))
		return
	}

	// A key reference: serve a real key so the pull path can validate it.
	// STUB_OP_READ_KEY overrides what reads return, which is how the harness
	// simulates a write that silently stored something else.
	keyPath := envOr("STUB_OP_READ_KEY", os.Getenv("STUB_OP_KEY"))
	if keyPath == "" {
		fatalf("stub: STUB_OP_KEY is not set, cannot serve a private key")
	}
	key, err := os.ReadFile(filepath.Clean(keyPath)) // #nosec G304 G703
	if err != nil {
		fatalf("stub: read key: %v", err)
	}
	fmt.Print(string(key))
}

// assertItemDocument fails the command when no usable item JSON arrived on
// stdin, which is exactly the failure the real CLI produces for a --template
// run: "cannot create an item from template and stdin at the same time" is the
// mirror image of this check.
//
// It also refuses an SSH_KEY payload outright. The real CLI accepts one, exits 0,
// and then stores nothing - which is how the original empty-item bug went
// unnoticed: a stub that only checked "was stdin non-empty" happily agreed. A
// hard failure here means any regression back to SSH Key items is caught by the
// harness instead of by a user's broken connection.
func assertItemDocument(stdin []byte, op string) {
	if len(strings.TrimSpace(string(stdin))) == 0 {
		fatalf("no item document received on stdin, so `op item %s` cannot proceed", op)
	}

	var doc map[string]any
	if err := json.Unmarshal(stdin, &doc); err != nil {
		fatalf("stdin is not valid item JSON: %v", err)
	}
	title, _ := doc["title"].(string)
	if title == "" {
		fatalf("item document has no title")
	}
	if category, _ := doc["category"].(string); strings.EqualFold(category, "SSH_KEY") || strings.EqualFold(category, "SSHKEY") {
		fatalf("item %q targets the SSH_KEY category; the 1Password CLI silently discards the private key on create and refuses to edit such items - a key belongs in a Login item's concealed field", title)
	}

	fields, _ := doc["fields"].([]any)
	seen := map[string]string{}
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := field["id"].(string)
		value, _ := field["value"].(string)
		if value != "" {
			seen[id] = "SET"
		}
	}
	if seen["opspulse_private_key"] == "" && seen["password"] == "" {
		fatalf("item %q carries neither a private key nor a password value", title)
	}
}

func readStdin() []byte {
	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice != 0 {
		return nil
	}
	data, _ := io.ReadAll(os.Stdin)
	return data
}

func logLine(path string, args []string, stdin []byte) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 G703
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	kind, category := "-", "-"
	var doc map[string]any
	if err := json.Unmarshal(stdin, &doc); err == nil {
		if title, _ := doc["title"].(string); title != "" {
			kind = title
		}
		if c, _ := doc["category"].(string); c != "" {
			category = c
		}
	}
	_, _ = fmt.Fprintf(f, "op %s | stdin=%d bytes | item=%s | category=%s | OP_ACCOUNT=%s\n",
		strings.Join(args, " "), len(stdin), kind, category, os.Getenv("OP_ACCOUNT"))
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func writeJSON(v any) {
	out, err := json.Marshal(v)
	if err != nil {
		fatalf("stub: marshal: %v", err)
	}
	fmt.Print(string(out))
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[ERROR] "+format+"\n", args...)
	os.Exit(1)
}
