package sftp

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/volcano6/opspulse/internal/server"
)

func TestNewClient_InvalidHost(t *testing.T) {
	srv := server.Server{
		Name:     "invalid-server",
		Host:     "192.0.2.1", // Test-Net-1 unrouteable
		Port:     2222,
		User:     "root",
		Password: "fake-password",
	}

	// Should fail connection quickly
	_, err := NewClient(srv, 50*time.Millisecond)
	if err == nil {
		t.Error("expected connection error for unrouteable host, got nil")
	}
}

func TestNewClient_NoAuth(t *testing.T) {
	srv := server.Server{
		Name:    "no-auth-server",
		Host:    "127.0.0.1",
		Port:    22,
		KeyPath: "/non/existent/key/path",
	}

	_, err := NewClient(srv, 100*time.Millisecond)
	if err == nil {
		t.Error("expected auth error for non-existent key path, got nil")
	}
}

// newTestClient returns a Client talking to an in-process SFTP server whose
// files live under the returned root.
//
// Remote paths handed to the client must be relative: the server resolves them
// under root, whereas an absolute path would address the real filesystem.
func newTestClient(t *testing.T) (*Client, string) {
	t.Helper()

	root := t.TempDir()
	serverConn, clientConn := net.Pipe()

	svr, err := sftp.NewServer(serverConn, sftp.WithServerWorkingDirectory(root))
	if err != nil {
		t.Fatalf("start in-process sftp server: %v", err)
	}
	go func() { _ = svr.Serve() }()

	conn, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("connect to in-process sftp server: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		_ = svr.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	return &Client{sftpClient: conn}, root
}

func TestWithinLocalBase(t *testing.T) {
	localDir := filepath.Join(t.TempDir(), "dest")

	tests := []struct {
		relPath string
		want    bool
	}{
		{"safe/file.txt", true},
		{"safe/subdir/subfile.txt", true},
		{"../escape.txt", false},
		{"../../etc/passwd", false},
		{"safe/../../escape.txt", false},
	}

	for _, tc := range tests {
		target := filepath.Join(localDir, filepath.FromSlash(tc.relPath))
		if got := withinLocalBase(localDir, target); got != tc.want {
			t.Errorf("withinLocalBase(%q, %q) = %v, want %v", localDir, target, got, tc.want)
		}
	}

	// The destination itself counts as inside, but a sibling directory whose
	// name merely starts with the same characters does not.
	if !withinLocalBase(localDir, localDir) {
		t.Errorf("the destination directory should be within itself")
	}
	if withinLocalBase(localDir, localDir+"-elsewhere/file.txt") {
		t.Errorf("a prefix-matching sibling directory must be rejected")
	}
}

func TestDownloadFile_MasksGroupAndOtherWriteBits(t *testing.T) {
	client, root := newTestClient(t)

	tests := []struct {
		name   string
		remote os.FileMode
		want   os.FileMode
	}{
		{"group and other write bits are dropped", 0o777, 0o755},
		{"a world-writable data file lands 0644", 0o666, 0o644},
		{"owner execute bit is preserved", 0o700, 0o700},
		{"an already narrow mode is preserved", 0o640, 0o640},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			remoteName := fmt.Sprintf("remote-%d.txt", i)
			content := []byte("payload")
			remoteAbs := filepath.Join(root, remoteName)

			if err := os.WriteFile(remoteAbs, content, tc.remote); err != nil {
				t.Fatalf("write remote file: %v", err)
			}
			if err := os.Chmod(remoteAbs, tc.remote); err != nil {
				t.Fatalf("chmod remote file: %v", err)
			}
			remoteInfo, err := os.Stat(remoteAbs)
			if err != nil {
				t.Fatalf("stat remote file: %v", err)
			}
			if remoteInfo.Mode().Perm() != tc.remote {
				t.Fatalf("remote mode = %v, want %v: the case would be vacuous", remoteInfo.Mode().Perm(), tc.remote)
			}

			localPath := filepath.Join(t.TempDir(), "local.txt")
			n, err := client.DownloadFile(remoteName, localPath)
			if err != nil {
				t.Fatalf("DownloadFile() error: %v", err)
			}
			if n != int64(len(content)) {
				t.Errorf("DownloadFile() copied %d bytes, want %d", n, len(content))
			}

			got, err := os.ReadFile(localPath)
			if err != nil {
				t.Fatalf("read local file: %v", err)
			}
			if !bytes.Equal(got, content) {
				t.Errorf("local content = %q, want %q", got, content)
			}

			info, err := os.Stat(localPath)
			if err != nil {
				t.Fatalf("stat local file: %v", err)
			}
			if info.Mode().Perm() != tc.want {
				t.Errorf("local mode = %v, want %v", info.Mode().Perm(), tc.want)
			}
		})
	}
}

func TestUploadFile_ReplacesExistingRemoteFile(t *testing.T) {
	client, root := newTestClient(t)

	remoteName := "app.conf"
	remoteAbs := filepath.Join(root, remoteName)
	if err := os.WriteFile(remoteAbs, []byte("old"), 0o600); err != nil {
		t.Fatalf("write remote file: %v", err)
	}

	localPath := filepath.Join(t.TempDir(), remoteName)
	const content = "new-content"
	if err := os.WriteFile(localPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	n, err := client.UploadFile(localPath, remoteName)
	if err != nil {
		t.Fatalf("UploadFile() error: %v", err)
	}
	if n != int64(len(content)) {
		t.Errorf("UploadFile() copied %d bytes, want %d", n, len(content))
	}

	got, err := os.ReadFile(remoteAbs)
	if err != nil {
		t.Fatalf("read remote file: %v", err)
	}
	if string(got) != content {
		t.Errorf("remote content = %q, want %q", got, content)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read remote directory: %v", err)
	}
	for _, e := range entries {
		if e.Name() != remoteName {
			t.Errorf("upload left %q behind, want only %q", e.Name(), remoteName)
		}
	}
}

func TestDownloadDir_CopiesTreeAndSkipsSymlinks(t *testing.T) {
	client, root := newTestClient(t)

	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatalf("create remote tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("write remote file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "nested.txt"), []byte("nested"), 0o600); err != nil {
		t.Fatalf("write nested remote file: %v", err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatalf("create remote symlink: %v", err)
	}

	localDir := filepath.Join(t.TempDir(), "dest")
	count, total, err := client.DownloadDir("src", localDir)
	if err != nil {
		t.Fatalf("DownloadDir() error: %v", err)
	}
	if count != 2 {
		t.Errorf("DownloadDir() copied %d files, want 2 (the symlink is not one)", count)
	}
	if want := int64(len("keep") + len("nested")); total != want {
		t.Errorf("DownloadDir() copied %d bytes, want %d", total, want)
	}

	for name, want := range map[string]string{
		"keep.txt":                         "keep",
		filepath.Join("sub", "nested.txt"): "nested",
	} {
		got, err := os.ReadFile(filepath.Join(localDir, name))
		if err != nil {
			t.Errorf("read downloaded %q: %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("downloaded %q = %q, want %q", name, got, want)
		}
	}

	// A symlink in the remote tree must not pull its target into the download.
	if _, err := os.Lstat(filepath.Join(localDir, "link.txt")); !os.IsNotExist(err) {
		t.Errorf("the remote symlink was materialised locally (lstat err: %v)", err)
	}
}

// fakeRemoteFS models the server the rename fallback exists for: one that does
// not implement the posix-rename extension and whose plain rename refuses to
// overwrite an existing destination.
type fakeRemoteFS struct {
	files     map[string]string
	posixErr  error
	renameErr func(oldpath, newpath string) error
	removed   []string
}

func newFakeRemoteFS(files map[string]string) *fakeRemoteFS {
	return &fakeRemoteFS{
		files:    files,
		posixErr: errors.New("posix-rename@openssh.com extension not supported"),
	}
}

func (f *fakeRemoteFS) PosixRename(oldpath, newpath string) error {
	if f.posixErr != nil {
		return f.posixErr
	}
	f.move(oldpath, newpath)
	return nil
}

func (f *fakeRemoteFS) Rename(oldpath, newpath string) error {
	if f.renameErr != nil {
		if err := f.renameErr(oldpath, newpath); err != nil {
			return err
		}
	}
	if _, exists := f.files[newpath]; exists {
		return fmt.Errorf("rename %q: file exists", newpath)
	}
	f.move(oldpath, newpath)
	return nil
}

func (f *fakeRemoteFS) Remove(path string) error {
	if _, ok := f.files[path]; !ok {
		return os.ErrNotExist
	}
	delete(f.files, path)
	f.removed = append(f.removed, path)
	return nil
}

func (f *fakeRemoteFS) Stat(path string) (os.FileInfo, error) {
	content, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return fakeFileInfo{name: path, size: int64(len(content))}, nil
}

func (f *fakeRemoteFS) move(oldpath, newpath string) {
	f.files[newpath] = f.files[oldpath]
	delete(f.files, oldpath)
}

func (f *fakeRemoteFS) parked() []string {
	var names []string
	for name := range f.files {
		if strings.Contains(name, ".opspulse-bak.") {
			names = append(names, name)
		}
	}
	return names
}

type fakeFileInfo struct {
	name string
	size int64
}

func (i fakeFileInfo) Name() string       { return i.name }
func (i fakeFileInfo) Size() int64        { return i.size }
func (i fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (i fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeFileInfo) IsDir() bool        { return false }
func (i fakeFileInfo) Sys() any           { return nil }

func TestInstallRemoteFile_PrefersPosixRename(t *testing.T) {
	fs := newFakeRemoteFS(map[string]string{"tmp": "new", "dest": "old"})
	fs.posixErr = nil

	if err := installRemoteFile(fs, "tmp", "dest"); err != nil {
		t.Fatalf("installRemoteFile() error: %v", err)
	}
	if fs.files["dest"] != "new" {
		t.Errorf("destination = %q, want the uploaded content", fs.files["dest"])
	}
	if _, ok := fs.files["tmp"]; ok {
		t.Errorf("the temporary file survived: %v", fs.files)
	}
	if len(fs.removed) != 0 {
		t.Errorf("the atomic path removed %v, want nothing", fs.removed)
	}
}

func TestInstallRemoteFile_ParksExistingFileDuringFallback(t *testing.T) {
	fs := newFakeRemoteFS(map[string]string{"tmp": "new", "dest": "old"})

	if err := installRemoteFile(fs, "tmp", "dest"); err != nil {
		t.Fatalf("installRemoteFile() error: %v", err)
	}
	if fs.files["dest"] != "new" {
		t.Errorf("destination = %q, want the uploaded content", fs.files["dest"])
	}
	if _, ok := fs.files["tmp"]; ok {
		t.Errorf("the temporary file survived: %v", fs.files)
	}
	if parked := fs.parked(); len(parked) != 0 {
		t.Errorf("parked copies left behind: %v", parked)
	}
	if len(fs.removed) != 1 || !strings.Contains(fs.removed[0], ".opspulse-bak.") {
		t.Errorf("removed = %v, want only the parked copy", fs.removed)
	}
}

func TestInstallRemoteFile_WithoutExistingDestination(t *testing.T) {
	fs := newFakeRemoteFS(map[string]string{"tmp": "new"})

	if err := installRemoteFile(fs, "tmp", "dest"); err != nil {
		t.Fatalf("installRemoteFile() error: %v", err)
	}
	if fs.files["dest"] != "new" {
		t.Errorf("destination = %q, want the uploaded content", fs.files["dest"])
	}
	if parked := fs.parked(); len(parked) != 0 {
		t.Errorf("parked copies created for a new file: %v", parked)
	}
}

// TestInstallRemoteFile_KeepsDestinationWhenRenameFails is the regression test
// for the delete-then-rename sequence: removing the destination before the
// fallback rename meant a failing rename destroyed the file that was there.
func TestInstallRemoteFile_KeepsDestinationWhenRenameFails(t *testing.T) {
	fs := newFakeRemoteFS(map[string]string{"tmp": "new", "dest": "old"})
	fs.renameErr = func(_, _ string) error { return errors.New("permission denied") }

	err := installRemoteFile(fs, "tmp", "dest")
	if err == nil {
		t.Fatal("expected an error when neither rename can be performed")
	}
	if fs.files["dest"] != "old" {
		t.Errorf("the existing destination was destroyed: %v", fs.files)
	}
	if fs.files["tmp"] != "new" {
		t.Errorf("the uploaded data was discarded: %v", fs.files)
	}
}

func TestInstallRemoteFile_RollsBackWhenFinalRenameFails(t *testing.T) {
	fs := newFakeRemoteFS(map[string]string{"tmp": "new", "dest": "old"})
	fs.renameErr = func(oldpath, _ string) error {
		if oldpath == "tmp" {
			return errors.New("no space left on device")
		}
		return nil
	}

	err := installRemoteFile(fs, "tmp", "dest")
	if err == nil {
		t.Fatal("expected an error when the uploaded file cannot be moved into place")
	}
	if fs.files["dest"] != "old" {
		t.Errorf("destination = %q, want the parked original restored", fs.files["dest"])
	}
	if _, ok := fs.files["tmp"]; ok {
		t.Errorf("the failed upload left its temporary file behind: %v", fs.files)
	}
	if parked := fs.parked(); len(parked) != 0 {
		t.Errorf("rollback left parked copies behind: %v", parked)
	}
}
