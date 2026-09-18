package secret

import (
	"encoding/json"
	"strings"
	"testing"
)

// sshKeyItemFixture mirrors `op item get <item> --format json` for an existing
// managed key item - a Login item carrying OpsPulse's custom concealed field -
// that the user has since annotated.
const sshKeyItemFixture = `{
  "id": "abc123",
  "title": "opspulse_web_key",
  "version": 3,
  "vault": {"id": "vault-uuid"},
  "category": "LOGIN",
  "tags": ["infra"],
  "fields": [
    {"id": "username", "type": "STRING", "label": "username", "purpose": "USERNAME", "value": ""},
    {"id": "password", "type": "CONCEALED", "label": "password", "purpose": "PASSWORD", "value": ""},
    {"id": "notesPlain", "type": "STRING", "label": "notesPlain", "purpose": "NOTES", "value": "rotate me"},
    {"id": "opspulse_private_key", "type": "CONCEALED", "label": "private key", "value": "OLD-KEY"}
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

// secureNoteTemplateFixture mirrors the document produced by
// `op item template get "Secure Note"`.
const secureNoteTemplateFixture = `{
  "title": "",
  "category": "SECURE_NOTE",
  "fields": [
    {"id": "notesPlain", "type": "STRING", "purpose": "NOTES", "label": "notesPlain", "value": ""}
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
	ref := BuildSSHKeyRef("Private", "opspulse_web_key")
	if want := "op://Private/opspulse_web_key/opspulse_private_key"; ref != want {
		t.Fatalf("BuildSSHKeyRef() = %q, want %q", ref, want)
	}
	// The key lives in a concealed text field, and `op` rejects ssh-format on
	// anything but a real SSHKEY field.
	if strings.Contains(ref, "ssh-format") {
		t.Errorf("BuildSSHKeyRef() = %q, must not carry ssh-format", ref)
	}

	vault, item, ok := ParseSSHKeyRef(ref)
	if !ok || vault != "Private" || item != "opspulse_web_key" {
		t.Errorf("ParseSSHKeyRef(%q) = (%q, %q, %v), want (Private, opspulse_web_key, true)", ref, vault, item, ok)
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
	// The key item and the password item must never share a title: both are
	// Login items, so a shared title would mean a push of one overwrites the
	// other's fields.
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
			name:  "leaves the managed concealed field untouched",
			input: "op://Private/opspulse_web_key/opspulse_private_key",
			want:  "op://Private/opspulse_web_key/opspulse_private_key",
		},
		{
			name:  "does not mistake an item name for the query parameter",
			input: "op://Private/opspulse_ssh-format/private key",
			want:  "op://Private/opspulse_ssh-format/private key?ssh-format=openssh",
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

func TestFillSSHKeyItemAppendsManagedField(t *testing.T) {
	payload, err := FillSSHKeyItem([]byte(loginTemplateFixture), "opspulse_web_key", "PRIVATE-KEY-DATA")
	if err != nil {
		t.Fatalf("FillSSHKeyItem() error: %v", err)
	}

	parsed := decodeDocument(t, payload)
	if parsed["title"] != "opspulse_web_key" {
		t.Errorf("title = %v, want opspulse_web_key", parsed["title"])
	}
	if parsed["category"] != "LOGIN" {
		t.Errorf("category = %v, want LOGIN", parsed["category"])
	}
	if _, present := parsed["vault"]; present {
		t.Error("vault should never be left in the payload, the command line selects it")
	}

	values := fieldValues(t, payload)
	if values[sshKeyManagedFieldID] != "PRIVATE-KEY-DATA" {
		t.Errorf("%s = %q, want PRIVATE-KEY-DATA", sshKeyManagedFieldID, values[sshKeyManagedFieldID])
	}
	// The key must not be smuggled into the built-in password field: that is
	// what the item's own Login semantics use, and clobbering it would surprise
	// anyone who also keeps a password there.
	if values[loginPasswordFieldID] != "" {
		t.Errorf("password = %q, want it left empty", values[loginPasswordFieldID])
	}
	if _, present := values[sshKeyPrivateFieldID]; present {
		t.Error("a Login item must not pretend to carry a real SSHKEY field")
	}
	if _, present := values["notesPlain"]; !present {
		t.Error("unrelated built-in fields should be preserved")
	}
}

func TestFillSSHKeyItemDoesNotDuplicateManagedField(t *testing.T) {
	// A re-push must overwrite the managed field, not append a second one: op
	// accepts duplicate ids and the reader would then depend on field order.
	first, err := FillSSHKeyItem([]byte(loginTemplateFixture), "opspulse_web_key", "OLD-KEY")
	if err != nil {
		t.Fatalf("FillSSHKeyItem() error: %v", err)
	}
	second, err := FillSSHKeyItem(first, "opspulse_web_key", "NEW-KEY")
	if err != nil {
		t.Fatalf("FillSSHKeyItem() error on the second pass: %v", err)
	}

	parsed := decodeDocument(t, second)
	fields, _ := parsed["fields"].([]any)
	count := 0
	for _, raw := range fields {
		field, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := field["id"].(string); id == sshKeyManagedFieldID {
			count++
			if value, _ := field["value"].(string); value != "NEW-KEY" {
				t.Errorf("managed field value = %q, want NEW-KEY", value)
			}
		}
	}
	if count != 1 {
		t.Errorf("managed field appears %d times, want exactly 1", count)
	}
}

func TestFillSSHKeyItemPreservesExistingItem(t *testing.T) {
	// Re-pushing a key must not wipe what the user added in 1Password, nor the
	// item's identity: `op item edit` consumes the whole document.
	payload, err := FillSSHKeyItem([]byte(sshKeyItemFixture), "opspulse_web_key", "NEW-KEY")
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
	if values[sshKeyManagedFieldID] != "NEW-KEY" {
		t.Errorf("%s = %q, want NEW-KEY", sshKeyManagedFieldID, values[sshKeyManagedFieldID])
	}
	if values["notesPlain"] != "rotate me" {
		t.Errorf("notesPlain = %q, want the user's note preserved", values["notesPlain"])
	}
}

func TestFillSSHKeyItemRejectsEmptyKeyOrInvalidJSON(t *testing.T) {
	if _, err := FillSSHKeyItem([]byte(loginTemplateFixture), "t", "   "); err == nil {
		t.Error("expected an error for an empty private key")
	}
	if _, err := FillSSHKeyItem([]byte("not json"), "t", "k"); err == nil {
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
	if got := SSHKeyItemTitle("web-01"); got != "opspulse_web-01_key" {
		t.Errorf("SSHKeyItemTitle() = %q, want opspulse_web-01_key", got)
	}
}

func TestBuildInventoryRef(t *testing.T) {
	if got := BuildInventoryRef("Personal"); got != "op://Personal/opspulse_inventory/notesPlain" {
		t.Errorf("BuildInventoryRef() = %q", got)
	}
}

// The inventory payload is YAML with significant indentation and trailing
// whitespace, so the round trip has to be byte-exact rather than "close enough".
func TestFillInventoryItem(t *testing.T) {
	yamlText := "servers:\n    - name: web\n      host: 10.0.0.10\n      user: ubuntu\n"

	payload, err := FillInventoryItem([]byte(secureNoteTemplateFixture), yamlText)
	if err != nil {
		t.Fatalf("FillInventoryItem() error: %v", err)
	}

	parsed := decodeDocument(t, payload)
	if parsed["title"] != InventoryItemTitle {
		t.Errorf("title = %v, want %v", parsed["title"], InventoryItemTitle)
	}
	if _, present := parsed["vault"]; present {
		t.Error("vault should have been removed")
	}
	if got := fieldValues(t, payload)[inventoryNoteFieldID]; got != yamlText {
		t.Errorf("notesPlain = %q, want %q", got, yamlText)
	}
}

// Re-using the item's own document on updates is what keeps user-added fields
// alive, so the fill must touch notesPlain and nothing else.
func TestFillInventoryItemPreservesExistingItem(t *testing.T) {
	existing := `{
	  "id": "abc123",
	  "title": "opspulse_inventory",
	  "version": 4,
	  "vault": {"id": "vault-uuid"},
	  "category": "SECURE_NOTE",
	  "tags": ["infra"],
	  "fields": [
	    {"id": "notesPlain", "type": "STRING", "purpose": "NOTES", "label": "notesPlain", "value": "stale"}
	  ]
	}`

	payload, err := FillInventoryItem([]byte(existing), "fresh")
	if err != nil {
		t.Fatalf("FillInventoryItem() error: %v", err)
	}

	parsed := decodeDocument(t, payload)
	if parsed["id"] != "abc123" || parsed["version"] != float64(4) {
		t.Errorf("identity was not preserved: id=%v version=%v", parsed["id"], parsed["version"])
	}
	tags, _ := parsed["tags"].([]any)
	if len(tags) != 1 || tags[0] != "infra" {
		t.Errorf("tags were not preserved: %v", parsed["tags"])
	}
	if got := fieldValues(t, payload)[inventoryNoteFieldID]; got != "fresh" {
		t.Errorf("notesPlain = %q, want fresh", got)
	}
}

func TestFillInventoryItemRejectsMissingNoteFieldOrEmptyValue(t *testing.T) {
	// A Secure Note template always carries notesPlain, so its absence means the
	// document is not what the caller assumed. Appending it would risk the exact
	// silent-drop shape that made SSH Key items unusable.
	sparse := `{"title": "", "category": "SECURE_NOTE", "fields": []}`
	if _, err := FillInventoryItem([]byte(sparse), "servers: []"); err == nil {
		t.Error("expected an error when the document has no notesPlain field")
	}
	if _, err := FillInventoryItem([]byte(secureNoteTemplateFixture), "   \n"); err == nil {
		t.Error("expected an error for an empty inventory")
	}
	if _, err := FillInventoryItem([]byte("not json"), "servers: []"); err == nil {
		t.Error("expected an error for an unparsable document")
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
