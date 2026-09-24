// Package sftp provides high-performance SFTP file and directory transfer capabilities.
package sftp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"golang.org/x/crypto/ssh"
)

// Client encapsulates an active SFTP session backed by an SSH connection.
type Client struct {
	sshClient  *ssh.Client
	sftpClient *sftp.Client
	cleanup    func()
}

// NewClient establishes an SSH connection and initializes an SFTP subsystem client.
func NewClient(srv server.Server, timeout time.Duration) (*Client, error) {
	return NewClientWithJumpAndWriter(srv, nil, timeout, nil)
}

// NewClientWithJumpAndWriter establishes an SSH connection (optionally through a jump host and with a custom warning writer) and initializes an SFTP subsystem client.
func NewClientWithJumpAndWriter(srv server.Server, jump *server.Server, timeout time.Duration, warnWriter io.Writer) (*Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	exec := executor.NewSSHExecutor().WithWarnWriter(warnWriter)
	exec.ConnectTimeout = timeout
	target := executor.NewServerTargetWithJump(srv, jump)

	sshConn, cleanup, err := exec.DialTarget(context.Background(), target)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s (%s): %w", srv.Name, srv.Address(), err)
	}

	sftpConn, err := sftp.NewClient(sshConn)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to initialize sftp subsystem on %s: %w", srv.Name, err)
	}

	return &Client{
		sshClient:  sshConn,
		sftpClient: sftpConn,
		cleanup:    cleanup,
	}, nil
}

// Close closes the underlying SFTP and SSH connections.
func (c *Client) Close() error {
	var errs []string
	if c.sftpClient != nil {
		if err := c.sftpClient.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.cleanup != nil {
		c.cleanup()
	} else if c.sshClient != nil {
		if err := c.sshClient.Close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("error closing sftp client: %s", strings.Join(errs, "; "))
	}
	return nil
}

// UploadFile uploads a single local file to the destination remote path.
func (c *Client) UploadFile(localPath, remotePath string) (int64, error) {
	srcFile, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to open local file %q: %w", localPath, err)
	}
	defer func() { _ = srcFile.Close() }()

	srcStat, err := srcFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat local file %q: %w", localPath, err)
	}
	if srcStat.IsDir() {
		return 0, fmt.Errorf("local path %q is a directory (use --recursive to upload directories)", localPath)
	}

	// If remotePath is a directory or ends in '/', append local filename
	if strings.HasSuffix(remotePath, "/") {
		remotePath = path.Join(remotePath, filepath.Base(localPath))
	} else if rStat, err := c.sftpClient.Stat(remotePath); err == nil && rStat.IsDir() {
		remotePath = path.Join(remotePath, filepath.Base(localPath))
	}

	// Ensure remote parent directory exists
	remoteDir := path.Dir(remotePath)
	if err := c.sftpClient.MkdirAll(remoteDir); err != nil {
		return 0, fmt.Errorf("failed to create remote directory %q: %w", remoteDir, err)
	}

	tmpRemote := fmt.Sprintf("%s.tmp.%d", remotePath, time.Now().UnixNano())
	dstFile, err := c.sftpClient.Create(tmpRemote)
	if err != nil {
		return 0, fmt.Errorf("failed to create remote temporary file %q: %w", tmpRemote, err)
	}

	progressReader := newProgressReader(srcFile, srcStat.Size(), "Uploading", localPath, remotePath)
	n, copyErr := io.Copy(dstFile, progressReader)
	closeErr := dstFile.Close()
	if copyErr != nil {
		_ = c.sftpClient.Remove(tmpRemote)
		return 0, fmt.Errorf("failed to upload data to %q: %w", remotePath, copyErr)
	}
	if closeErr != nil {
		_ = c.sftpClient.Remove(tmpRemote)
		return 0, fmt.Errorf("failed to close remote temporary file %q: %w", tmpRemote, closeErr)
	}

	if err := c.sftpClient.Chmod(tmpRemote, srcStat.Mode().Perm()); err != nil {
		_ = c.sftpClient.Remove(tmpRemote)
		return n, fmt.Errorf("failed to set remote file permissions on %q: %w", tmpRemote, err)
	}

	if err := installRemoteFile(c.sftpClient, tmpRemote, remotePath); err != nil {
		return n, err
	}
	return n, nil
}

// remoteFileOps is the slice of *sftp.Client that the atomic install sequence
// needs. It exists so the fallback path can be exercised against a fake server
// (one without the posix-rename extension) in tests.
type remoteFileOps interface {
	PosixRename(oldpath, newpath string) error
	Rename(oldpath, newpath string) error
	Remove(path string) error
	Stat(path string) (os.FileInfo, error)
}

// installRemoteFile moves tmpPath onto destPath, replacing whatever is there.
//
// posix-rename is preferred because it is atomic and overwrites the
// destination in one step. Servers without that extension fall back to plain
// Rename, which refuses to overwrite: the old destination is parked at a
// sibling name first and only removed once the new file is in place, so a
// failing destination rename costs nothing. Deleting the destination up front
// (as an earlier version did) lost the file whenever the fallback also failed.
func installRemoteFile(fs remoteFileOps, tmpPath, destPath string) error {
	if err := fs.PosixRename(tmpPath, destPath); err == nil {
		return nil
	}

	// The destination is parked rather than deleted: the only copy of the old
	// file survives a failed fallback rename, and the parked copy is dropped
	// after the new one is in place.
	backupPath := fmt.Sprintf("%s.opspulse-bak.%d", destPath, time.Now().UnixNano())
	parked := false
	if _, err := fs.Stat(destPath); err == nil {
		if err := fs.Rename(destPath, backupPath); err != nil {
			return fmt.Errorf("failed to replace %q: cannot park the existing file: %w", destPath, err)
		}
		parked = true
	}

	if err := fs.Rename(tmpPath, destPath); err != nil {
		if parked {
			if rbErr := fs.Rename(backupPath, destPath); rbErr != nil {
				// Both copies are left in place on purpose: the destination is
				// missing, so deleting either one would discard the only copy
				// of something.
				return fmt.Errorf("failed to rename %q to %q: %w (the previous file is parked at %q and could not be restored: %v)", tmpPath, destPath, err, backupPath, rbErr)
			}
		}
		_ = fs.Remove(tmpPath)
		return fmt.Errorf("failed to rename %q to %q: %w", tmpPath, destPath, err)
	}

	if parked {
		_ = fs.Remove(backupPath)
	}
	return nil
}

// DownloadFile downloads a single remote file to the destination local path.
func (c *Client) DownloadFile(remotePath, localPath string) (int64, error) {
	srcFile, err := c.sftpClient.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open remote file %q: %w", remotePath, err)
	}
	defer func() { _ = srcFile.Close() }()

	srcStat, err := srcFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat remote file %q: %w", remotePath, err)
	}
	if srcStat.IsDir() {
		return 0, fmt.Errorf("remote path %q is a directory (use --recursive to download directories)", remotePath)
	}

	// If localPath is a directory or ends in separator, append remote filename
	if strings.HasSuffix(localPath, "/") || strings.HasSuffix(localPath, "\\") {
		localPath = filepath.Join(localPath, path.Base(remotePath))
	} else if lStat, err := os.Stat(localPath); err == nil && lStat.IsDir() {
		localPath = filepath.Join(localPath, path.Base(remotePath))
	}

	// Ensure local parent directory exists
	localDir := filepath.Dir(localPath)
	if err := os.MkdirAll(localDir, 0o750); err != nil {
		return 0, fmt.Errorf("failed to create local directory %q: %w", localDir, err)
	}

	tmpFile, err := os.CreateTemp(localDir, filepath.Base(localPath)+".tmp.*")
	if err != nil {
		return 0, fmt.Errorf("failed to create local temporary file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpName)
	}()

	progressReader := newProgressReader(srcFile, srcStat.Size(), "Downloading", remotePath, localPath)
	n, copyErr := io.Copy(tmpFile, progressReader)
	if copyErr != nil {
		return 0, fmt.Errorf("failed to download data to %q: %w", localPath, copyErr)
	}

	if err := tmpFile.Chmod(localPermFromRemote(srcStat.Mode())); err != nil {
		return n, fmt.Errorf("failed to set local file permissions on %q: %w", tmpName, err)
	}
	if err := tmpFile.Close(); err != nil {
		return n, fmt.Errorf("failed to close local temporary file %q: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, localPath); err != nil {
		return n, fmt.Errorf("failed to rename %q to %q: %w", tmpName, localPath, err)
	}
	return n, nil
}

// localPermFromRemote maps a remote file mode onto the local copy's
// permissions: the owner bits are kept (an executable stays executable) while
// group and other write bits are dropped. The local file is created 0600 by
// os.CreateTemp, so copying a remote 0777 verbatim would only ever widen
// access to the downloaded file.
func localPermFromRemote(mode os.FileMode) os.FileMode {
	return mode.Perm() &^ 0o022
}

// UploadDir recursively uploads a local directory to a remote directory.
func (c *Client) UploadDir(localDir, remoteDir string) (int, int64, error) {
	localDir = filepath.Clean(localDir)
	info, err := os.Stat(localDir)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to stat local directory %q: %w", localDir, err)
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("local path %q is not a directory", localDir)
	}

	var filesCount int
	var totalBytes int64

	err = filepath.Walk(localDir, func(currPath string, currInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(localDir, currPath)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		// Convert local relPath to remote unix style path
		remoteTarget := path.Join(remoteDir, filepath.ToSlash(relPath))

		if currInfo.IsDir() {
			return c.sftpClient.MkdirAll(remoteTarget)
		}

		n, err := c.UploadFile(currPath, remoteTarget)
		if err != nil {
			return err
		}
		filesCount++
		totalBytes += n
		return nil
	})

	if err != nil {
		return filesCount, totalBytes, err
	}
	return filesCount, totalBytes, nil
}

// DownloadDir recursively downloads a remote directory to a local directory.
func (c *Client) DownloadDir(remoteDir, localDir string) (int, int64, error) {
	remoteDir = path.Clean(remoteDir)
	rStat, err := c.sftpClient.Stat(remoteDir)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to stat remote directory %q: %w", remoteDir, err)
	}
	if !rStat.IsDir() {
		return 0, 0, fmt.Errorf("remote path %q is not a directory", remoteDir)
	}

	var filesCount int
	var totalBytes int64

	walker := c.sftpClient.Walk(remoteDir)
	for walker.Step() {
		if walker.Err() != nil {
			return filesCount, totalBytes, walker.Err()
		}

		currRemote := walker.Path()
		relPath, err := filepath.Rel(filepath.FromSlash(remoteDir), filepath.FromSlash(currRemote))
		if err != nil || relPath == "." {
			continue
		}

		localTarget := filepath.Join(localDir, filepath.FromSlash(relPath))
		if !withinLocalBase(localDir, localTarget) {
			return filesCount, totalBytes, fmt.Errorf("path traversal detected: remote path %q resolves outside local destination %q", currRemote, localDir)
		}

		stat := walker.Stat()
		if stat.Mode()&os.ModeSymlink != 0 {
			// Skip symlinks to prevent unexpected traversal or dangling links
			continue
		}

		if stat.IsDir() {
			if err := os.MkdirAll(localTarget, 0o750); err != nil {
				return filesCount, totalBytes, err
			}
			continue
		}

		n, err := c.DownloadFile(currRemote, localTarget)
		if err != nil {
			return filesCount, totalBytes, err
		}
		filesCount++
		totalBytes += n
	}

	return filesCount, totalBytes, nil
}

// withinLocalBase reports whether target resolves to base itself or to a path
// inside it. It is the guard that keeps a remote-supplied path from writing
// outside the download destination; it lives in its own function so tests can
// exercise the real check instead of restating it.
func withinLocalBase(base, target string) bool {
	cleanBase := filepath.Clean(base)
	cleanTarget := filepath.Clean(target)
	return cleanTarget == cleanBase || strings.HasPrefix(cleanTarget, cleanBase+string(os.PathSeparator))
}
