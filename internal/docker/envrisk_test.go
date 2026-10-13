package docker

import (
	"reflect"
	"testing"
)

func TestIsSensitiveEnvName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"MYSQL_ROOT_PASSWORD", true},
		{"POSTGRES_PASSWORD", true},
		{"db_password", true},
		{"MYSQL_PWD", true},
		{"PWD", false}, // the shell's working directory, not a password
		{"GITHUB_TOKEN", true},
		{"SLACK_WEBHOOK_TOKEN", true},
		{"AWS_SECRET_ACCESS_KEY", true},
		{"AWS_ACCESS_KEY_ID", true},
		{"MY_APIKEY", true},
		{"OPENAI_API_KEY", true},
		{"SSH_PRIVATE_KEY", true},
		{"AZURE_CREDENTIALS", true},
		{"DB_PASSPHRASE", true},
		{"TZ", false},
		{"LANG", false},
		{"KEYBOARD_LAYOUT", false},
		{"MONKEY", false},
		{"AUTHOR", false},
		{"", false},
		{"   ", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSensitiveEnvName(tc.name); got != tc.want {
				t.Errorf("IsSensitiveEnvName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestSensitiveEnvVars(t *testing.T) {
	info := &ContainerInfo{
		Env: []string{
			"PATH=/usr/local/sbin:/usr/local/bin",
			"MYSQL_ROOT_PASSWORD=supersecret",
			"MYSQL_DATABASE=blog",
			"API_TOKEN=abc123",
			"MYSQL_ROOT_PASSWORD=supersecret", // duplicate entry
			"TZ=UTC",
		},
	}

	got := SensitiveEnvVars(info)
	want := []string{"API_TOKEN", "MYSQL_ROOT_PASSWORD"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SensitiveEnvVars() = %v, want %v", got, want)
	}

	// The returned names must never carry the values along.
	for _, name := range got {
		if name == "supersecret" || name == "abc123" {
			t.Fatalf("SensitiveEnvVars() returned a value instead of a name: %v", got)
		}
	}
}

func TestSensitiveEnvVarsEmpty(t *testing.T) {
	if got := SensitiveEnvVars(nil); got != nil {
		t.Errorf("SensitiveEnvVars(nil) = %v, want nil", got)
	}
	if got := SensitiveEnvVars(&ContainerInfo{}); got != nil {
		t.Errorf("SensitiveEnvVars(empty) = %v, want nil", got)
	}
	if got := SensitiveEnvVars(&ContainerInfo{Env: []string{"TZ=UTC", "LANG=C"}}); got != nil {
		t.Errorf("SensitiveEnvVars(benign) = %v, want nil", got)
	}
}
