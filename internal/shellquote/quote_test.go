package shellquote

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestValidEnvName(t *testing.T) {
	valid := []string{
		"FOO", "foo", "FOO_BAR", "_FOO", "RESTIC_PASSWORD", "A1", "var_123",
	}
	for _, name := range valid {
		if !ValidEnvName(name) {
			t.Errorf("expected ValidEnvName(%q) to be true", name)
		}
	}

	invalid := []string{
		"", "1A", "FOO-BAR", "FOO.BAR", "FOO BAR", "FOO=BAR", "FOO$BAR", "FOO;BAR", "FOO\nBAR",
	}
	for _, name := range invalid {
		if ValidEnvName(name) {
			t.Errorf("expected ValidEnvName(%q) to be false", name)
		}
	}
}

func TestQuote(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", "''"},
		{"hello", "'hello'"},
		{"hello world", "'hello world'"},
		{"it's fine", "'it'\\''s fine'"},
		{"$VAR", "'$VAR'"},
		{"pa$$word", "'pa$$word'"},
		{"`whoami`", "'`whoami`'"},
		{"$(cat /etc/passwd)", "'$(cat /etc/passwd)'"},
		{"foo; rm -rf /", "'foo; rm -rf /'"},
		{"foo\nbar", "'foo\nbar'"},
		{"-flag", "'-flag'"},
		{"\\back\\slash", "'\\back\\slash'"},
	}

	for _, tc := range cases {
		got := Quote(tc.input)
		if got != tc.want {
			t.Errorf("Quote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestQuote_ShellExecution(t *testing.T) {
	// Find sh or bash
	shPath, err := exec.LookPath("bash")
	if err != nil {
		shPath, err = exec.LookPath("sh")
		if err != nil {
			t.Skip("neither bash nor sh found in PATH")
		}
	}

	testInputs := []string{
		"hello world",
		"pa$$word",
		"$HOME",
		"$(echo evil)",
		"`echo evil`",
		"single'quote",
		"\"double-quote\"",
		"semi;colon",
		"pipe|char",
		"amp&ersand",
		"-leading-dash",
		"new\nline",
		"tab\tchar",
		"\\backslash\\",
		"combo ' \" $ ` ; \\ \n end",
	}

	for _, input := range testInputs {
		quoted := Quote(input)
		cmd := exec.Command(shPath, "-s")
		cmd.Stdin = strings.NewReader("printf '%s' " + quoted)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to run shell with quoted input %q: %v", input, err)
		}
		got := out.String()
		if got != input {
			t.Errorf("Shell fidelity mismatch for %q:\ngot:  %q\nwant: %q", input, got, input)
		}
	}
}
