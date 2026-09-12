package secret

import (
	"encoding/json"
	"testing"
)

// sshKeyTemplateFixture mirrors the document produced by
// `op item template get "SSH Key"` on a real CLI: the private key is the only
// value-bearing field, and there is no vault placeholder.
const sshKeyTemplateFixture = `{
  "title": "Imported Key",
  "category": "SSH_KEY",
  "fields": [
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "value": ""},
    {"id": "private_key", "type": "SSHKEY", "label": "private key", "value": ""}
  ]
}`

// sshKeyItemFixture mirrors `op item get <item> --format json` for an existing
// managed item that the user has since annotated.
const sshKeyItemFixture = `{
  "id": "abc123",
  "title": "opspulse_web",
  "version": 3,
  "vault": {"id": "vault-uuid"},
  "category": "SSH_KEY",
  "tags": ["infra"],
  "fields": [
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "value": "rotate me"},
    {"id": "private_key", "type": "SSHKEY", "label": "private key", "value": "OLD-KEY"},
    {"id": "public_key", "type": "STRING", "label": "public key", "value": "ssh-ed25519 OLD"}
  ]
}`

// loginTemplateFixture mirrors the document produced by
// `op item template get Login`.
const loginTemplateFixture = `{
  "title": "",
  "category": "LOGIN",
  "fields": [
    {"id": "username", "type": "STRING", "label": "username", "purpose": "USERNAME", "value": ""},
    {"id": "password", "type": "CONCEALED", "label": "password", "purpose": "PASSWORD", "value": ""},
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "purpose": "NOTES", "value": ""}
  ]
}`

func fieldValues(t *testing.T, payload []byte) map[string]string {
	t.Helper()

	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("produced document is not valid JSON: %v\n%s", err, payload)
	}
	fields, _ := parsed["fields"].([]any)
	values := map[string]string{}
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := field["id"].(string)
		value, _ := field["value"].(string)
		values[id] = value
	}
	return values
}

func decodeDocument(t *testing.T, payload []byte) map[string]any {
	t.Helper()

	var parsed map[string]any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		t.Fatalf("produced document is not valid JSON: %v\n%s", err, payload)
	}
	return parsed
}

func TestIs1PRef(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"op://Private/item/private key", true},
		{"  op://Private/item/private key  ", true},
		{"~/.ssh/id_ed25519", false},
		{"", false},
		{"OP://Private/item", false},
	}
	for _, tc := range tests {
		if got := Is1PRef(tc.input); got != tc.want {
			t.Errorf("Is1PRef(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestSSHKeyRefRoundTrip(t *testing.T) {
	ref := BuildSSHKeyRef("Private", "opspulse_web")
	if want := "op://Private/opspulse_web/private key"; ref != want {
		t.Fatalf("BuildSSHKeyRef() = %q, want %q", ref, want)
	}

	vault, item, ok := ParseSSHKeyRef(ref)
	if !ok || vault != "Private" || item != "opspulse_web" {
		t.Errorf("ParseSSHKeyRef(%q) = (%q, %q, %v), want (Private, opspulse_web, true)", ref, vault, item, ok)
	}

	if _, _, ok := ParseSSHKeyRef("~/.ssh/id_ed25519"); ok {
		t.Error("ParseSSHKeyRef should reject non op:// values")
	}
	if _, _, ok := ParseSSHKeyRef("op://Private"); ok {
		t.Error("ParseSSHKeyRef should reject references without an item segment")
	}
}

func TestPasswordRefRoundTrip(t *testing.T) {
	if got := PasswordItemTitle("vps_01"); got != "opspulse_vps_01_password" {
		t.Fatalf("PasswordItemTitle() = %q", got)
	}
	// The key item and the password item must never share a title, otherwise a
	// Login item would replace the pushed SSH key.
	if PasswordItemTitle("vps_01") == SSHKeyItemTitle("vps_01") {
		t.Fatal("password and SSH key items must not share a title")
	}

	ref := BuildPasswordRef("Personal", PasswordItemTitle("vps_01"))
	if want := "op://Personal/opspulse_vps_01_password/password"; ref != want {
		t.Fatalf("BuildPasswordRef() = %q, want %q", ref, want)
	}

	vault, item, field, ok := Parse1PRef(ref)
	if !ok || vault != "Personal" || item != "opspulse_vps_01_password" || field != PasswordItemLabel {
		t.Errorf("Parse1PRef(%q) = (%q, %q, %q, %v)", ref, vault, item, field, ok)
	}
}

func TestParse1PRefStripsQueryString(t *testing.T) {
	vault, item, field, ok := Parse1PRef("op://Private/opspulse_web/private key?ssh-format=openssh")
	if !ok || vault != "Private" || item != "opspulse_web" || field != "private key" {
		t.Errorf("Parse1PRef() = (%q, %q, %q, %v), want the field without the query", vault, item, field, ok)
	}

	if _, _, _, ok := Parse1PRef("op://Private"); ok {
		t.Error("a reference without an item segment must be rejected")
	}
}

func TestSSHKeyRefWithFormat(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "adds query parameter",
			input: "op://Private/opspulse_web/private key",
			want:  "op://Private/opspulse_web/private key?ssh-format=openssh",
		},
		{
			name:  "preserves an existing query string",
			input: "op://Private/opspulse_web/private key?attr=otp",
			want:  "op://Private/opspulse_web/private key?attr=otp&ssh-format=openssh",
		},
		{
			name:  "leaves an explicit format untouched",
			input: "op://Private/opspulse_web/private key?ssh-format=openssh",
			want:  "op://Private/opspulse_web/private key?ssh-format=openssh",
		},
		{
			name:    "rejects non references",
			input:   "~/.ssh/id_ed25519",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SSHKeyRefWithFormat(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got %q", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("SSHKeyRefWithFormat(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestFillSSHKeyItemFromTemplate(t *testing.T) {
	payload, err := FillSSHKeyItem([]byte(sshKeyTemplateFixture), "opspulse_web", "PRIVATE-KEY-DATA", "ssh-ed25519 AAAA opspulse:web")
	if err != nil {
		t.Fatalf("FillSSHKeyItem() error: %v", err)
	}

	parsed := decodeDocument(t, payload)
	if parsed["title"] != "opspulse_web" {
		t.Errorf("title = %v, want opspulse_web", parsed["title"])
	}
	if parsed["category"] != "SSH_KEY" {
		t.Errorf("category = %v, want SSH_KEY", parsed["category"])
	}
	if _, present := parsed["vault"]; present {
		t.Error("vault should never be left in the payload, the command line selects it")
	}

	values := fieldValues(t, payload)
	if values["private_key"] != "PRIVATE-KEY-DATA" {
		t.Errorf("private_key = %q, want PRIVATE-KEY-DATA", values["private_key"])
	}
	// 1Password derives the public key from the private key and the stock
	// template has no field for it, so no custom field may be invented.
	if _, present := values["public_key"]; present {
		t.Error("public_key must not be invented when the template has no such field")
	}
	if _, present := values["notesPlain"]; !present {
		t.Error("unrelated built-in fields should be preserved")
	}
}

func TestFillSSHKeyItemPreservesExistingItem(t *testing.T) {
	// Re-pushing a key must not wipe what the user added in 1Password, nor the
	// item's identity: `op item edit` consumes the whole document.
	payload, err := FillSSHKeyItem([]byte(sshKeyItemFixture), "opspulse_web", "NEW-KEY", "ssh-ed25519 NEW")
	if err != nil {
		t.Fatalf("FillSSHKeyItem() error: %v", err)
	}

	parsed := decodeDocument(t, payload)
	if parsed["id"] != "abc123" {
		t.Errorf("id = %v, want abc123 (the item's identity must survive)", parsed["id"])
	}
	if parsed["version"] == nil {
		t.Error("version should be preserved so the update is not rejected as stale")
	}
	if _, present := parsed["vault"]; present {
		t.Error("vault should be dropped so it cannot disagree with --vault")
	}
	if tags, _ := parsed["tags"].([]any); len(tags) != 1 || tags[0] != "infra" {
		t.Errorf("tags = %v, want them preserved", parsed["tags"])
	}

	values := fieldValues(t, payload)
	if values["private_key"] != "NEW-KEY" {
		t.Errorf("private_key = %q, want NEW-KEY", values["private_key"])
	}
	if values["public_key"] != "ssh-ed25519 NEW" {
		t.Errorf("public_key = %q, want it refreshed when the document has the field", values["public_key"])
	}
	if values["notesPlain"] != "rotate me" {
		t.Errorf("notesPlain = %q, want the user's note preserved", values["notesPlain"])
	}
}

func TestFillSSHKeyItemRejectsMissingKeyField(t *testing.T) {
	// A document without the SSHKEY field would silently produce an item with no
	// key, so this has to fail loudly instead.
	sparse := `{"title": "", "category": "SSH_KEY", "fields": [{"id": "notesPlain", "type": "STRING", "value": ""}]}`

	if _, err := FillSSHKeyItem([]byte(sparse), "opspulse_db", "KEY", ""); err == nil {
		t.Error("expected an error when the document has no private_key field")
	}
}

func TestFillSSHKeyItemRejectsEmptyKeyOrInvalidJSON(t *testing.T) {
	if _, err := FillSSHKeyItem([]byte(sshKeyTemplateFixture), "t", "   ", ""); err == nil {
		t.Error("expected an error for an empty private key")
	}
	if _, err := FillSSHKeyItem([]byte("not json"), "t", "k", ""); err == nil {
		t.Error("expected an error for an unparsable document")
	}
}

func TestFillLoginItem(t *testing.T) {
	payload, err := FillLoginItem([]byte(loginTemplateFixture), PasswordItemTitle("vps_01"), "root", "hunter2")
	if err != nil {
		t.Fatalf("FillLoginItem() error: %v", err)
	}

	parsed := decodeDocument(t, payload)
	if parsed["title"] != "opspulse_vps_01_password" {
		t.Errorf("title = %v", parsed["title"])
	}
	if _, present := parsed["vault"]; present {
		t.Error("vault should have been removed")
	}

	values := fieldValues(t, payload)
	if values[loginUsernameFieldID] != "root" {
		t.Errorf("username = %q, want root", values[loginUsernameFieldID])
	}
	if values[loginPasswordFieldID] != "hunter2" {
		t.Errorf("password = %q, want hunter2", values[loginPasswordFieldID])
	}
}

func TestFillLoginItemRejectsMissingPasswordOrEmptyValue(t *testing.T) {
	sparse := `{"title": "", "category": "LOGIN", "fields": [{"id": "username", "type": "STRING", "value": ""}]}`
	if _, err := FillLoginItem([]byte(sparse), "t", "root", "pw"); err == nil {
		t.Error("expected an error when the document has no password field")
	}
	if _, err := FillLoginItem([]byte(loginTemplateFixture), "t", "root", "  "); err == nil {
		t.Error("expected an error for an empty password")
	}
}

func TestItemTitles(t *testing.T) {
	if got := SSHKeyItemTitle("web-01"); got != "opspulse_web-01" {
		t.Errorf("SSHKeyItemTitle() = %q, want opspulse_web-01", got)
	}
}

func TestDetectReturnsUsableHandle(t *testing.T) {
	cli := Detect()
	if cli.Available() && cli.Path == "" {
		t.Error("an available CLI must carry a path")
	}
	if !cli.Available() && cli.IsWindowsBinary {
		t.Error("an unavailable CLI must not claim to be a Windows binary")
	}
}
