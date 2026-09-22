package secret

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func TestItemTitles(t *testing.T) {
	if got := SSHKeyItemTitle("web-01"); got != "opspulse_web-01_key" {
		t.Errorf("SSHKeyItemTitle() = %q, want opspulse_web-01_key", got)
	}
}

func TestBuildInventoryRef(t *testing.T) {
	if got := BuildInventoryRef("Personal", InventoryItemTitleFor("box")); got != "op://Personal/opspulse_inventory_box/notesPlain" {
		t.Errorf("BuildInventoryRef() = %q", got)
	}
	// The historical shared item is still readable, so its reference has to
	// keep working.
	if got := BuildInventoryRef("Personal", InventoryItemTitle); got != "op://Personal/opspulse_inventory/notesPlain" {
		t.Errorf("BuildInventoryRef() = %q", got)
	}
}

// Every per-machine title has to stay recognisable as an inventory item: the
// prefix is what restore uses to find backups, and what stops a backup item from
// being reported as an orphaned credential.
func TestInventoryItemTitles(t *testing.T) {
	if got := InventoryItemTitleFor("box"); got != "opspulse_inventory_box" {
		t.Errorf("InventoryItemTitleFor() = %q", got)
	}
	for _, title := range []string{InventoryItemTitle, InventoryItemTitleFor("box")} {
		if !IsInventoryItemTitle(title) {
			t.Errorf("IsInventoryItemTitle(%q) = false", title)
		}
	}
	for _, title := range []string{SSHKeyItemTitle("web"), PasswordItemTitle("web"), "notes"} {
		if IsInventoryItemTitle(title) {
			t.Errorf("IsInventoryItemTitle(%q) = true", title)
		}
	}
}

// The backup payload is YAML with significant indentation and trailing
// whitespace, so the round trip has to be byte-exact rather than "close enough".
func TestBuildInventoryItem(t *testing.T) {
	payload := "version: 1\nservers:\n    - name: web\n      host: 10.0.0.10\n"
	title := InventoryItemTitleFor("box")

	doc, err := BuildInventoryItem(title, payload)
	if err != nil {
		t.Fatalf("BuildInventoryItem() error: %v", err)
	}

	parsed := decodeDocument(t, doc)
	if parsed["title"] != title {
		t.Errorf("title = %v, want %v", parsed["title"], title)
	}
	if parsed["category"] != inventoryItemCategoryID {
		t.Errorf("category = %v, want %v", parsed["category"], inventoryItemCategoryID)
	}
	if _, present := parsed["vault"]; present {
		t.Error("vault must never be left in the payload, the command line selects it")
	}
	if got := fieldValues(t, doc)[inventoryNoteFieldID]; got != payload {
		t.Errorf("notesPlain = %q, want %q", got, payload)
	}
}

func TestBuildInventoryItemRejectsEmptyPayload(t *testing.T) {
	if _, err := BuildInventoryItem(InventoryItemTitleFor("box"), "   \n"); err == nil {
		t.Error("expected an error for an empty inventory")
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
