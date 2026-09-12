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

const existingItem = `{
  "id": "item-existing",
  "title": "opspulse_web",
  "version": 7,
  "vault": {"id": "vault-uuid"},
  "category": "SSH_KEY",
  "tags": ["infra"],
  "fields": [
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "value": "user note"},
    {"id": "private_key", "type": "SSHKEY", "label": "private key", "value": "OLD"},
    {"id": "public_key", "type": "STRING", "label": "public key", "value": "ssh-ed25519 OLD"}
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
		existing := os.Getenv("STUB_OP_EXISTING")
		if existing == "" {
			writeJSON([]map[string]string{})
			return
		}
		writeJSON([]map[string]string{{"id": "item-existing", "title": existing}})

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
	keyPath := os.Getenv("STUB_OP_KEY")
	if keyPath == "" {
		fatalf("stub: STUB_OP_KEY is not set, cannot serve a private key")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		fatalf("stub: read key: %v", err)
	}
	fmt.Print(string(key))
}

// assertItemDocument fails the command when no usable item JSON arrived on
// stdin, which is exactly the failure the real CLI produces for a --template
// run: "cannot create an item from template and stdin at the same time" is the
// mirror image of this check.
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
	if seen["private_key"] == "" && seen["password"] == "" {
		fatalf("item %q carries neither a private_key nor a password value", title)
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
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	kind := "-"
	var doc map[string]any
	if err := json.Unmarshal(stdin, &doc); err == nil {
		if title, _ := doc["title"].(string); title != "" {
			kind = title
		}
	}
	_, _ = fmt.Fprintf(f, "op %s | stdin=%d bytes | item=%s | OP_ACCOUNT=%s\n",
		strings.Join(args, " "), len(stdin), kind, os.Getenv("OP_ACCOUNT"))
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
