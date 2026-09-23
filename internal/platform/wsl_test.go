package platform

import "testing"

func TestToWSLPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`C:\Users\user`, "/mnt/c/Users/user"},
		{`D:\Projects\app`, "/mnt/d/Projects/app"},
		{`C:\Users\user\AppData\Local\Temp`, "/mnt/c/Users/user/AppData/Local/Temp"},
		{"no_drive_letter", "no_drive_letter"},
		{"/already/posix", "/already/posix"},
		{`  C:\spaced  `, "/mnt/c/spaced"},
		{"", ""},
	}

	for _, tc := range tests {
		if got := ToWSLPath(tc.input); got != tc.want {
			t.Errorf("ToWSLPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestToWindowsPath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/mnt/c/Users/user", `C:\Users\user`},
		{"/mnt/d/Projects/app", `D:\Projects\app`},
		{"/home/user", "/home/user"},
		{"/mnt/", "/mnt/"},
		{"", ""},
	}

	for _, tc := range tests {
		if got := ToWindowsPath(tc.input); got != tc.want {
			t.Errorf("ToWindowsPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPathTranslationRoundTrip(t *testing.T) {
	win := `C:\Users\user\AppData\Local\Temp`
	if got := ToWindowsPath(ToWSLPath(win)); got != win {
		t.Errorf("round trip of %q produced %q", win, got)
	}

	wsl := "/mnt/d/Projects/app"
	if got := ToWSLPath(ToWindowsPath(wsl)); got != wsl {
		t.Errorf("round trip of %q produced %q", wsl, got)
	}
}

func TestFileExists(t *testing.T) {
	if FileExists("/definitely/not/here/opspulse") {
		t.Error("FileExists reported a nonexistent path as present")
	}
	if FileExists(t.TempDir()) {
		t.Error("FileExists should report directories as absent")
	}
}

func TestWSLFMaskPattern(t *testing.T) {
	matches := []string{
		`options = "metadata,umask=22,fmask=011"`,
		`options = "metadata,umask=22,fmask=11"`,
		"fmask = 011",
		"fmask=011",
		"FMASK=011",
	}
	for _, content := range matches {
		if !wslFMaskPattern.MatchString(content) {
			t.Errorf("expected fmask pattern to match %q", content)
		}
	}

	nonMatches := []string{
		`options = "metadata,umask=022,fmask=000"`,
		`options = "metadata,umask=022,fmask=0110"`,
		"[automount]\nenabled = true",
	}
	for _, content := range nonMatches {
		if wslFMaskPattern.MatchString(content) {
			t.Errorf("expected fmask pattern NOT to match %q", content)
		}
	}
}
