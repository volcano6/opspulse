// Package sftp provides high-performance SFTP file and directory transfer capabilities.
package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/volcano6/opspulse/internal/executor"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/shellquote"
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
	return c.UploadFileContext(context.Background(), localPath, remotePath)
}

// UploadFileContext uploads a single local file and aborts on context
// cancellation. The remote temporary file is removed before returning.
func (c *Client) UploadFileContext(ctx context.Context, localPath, remotePath string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

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
	} else if rStat, statErr := c.sftpClient.Stat(remotePath); statErr == nil && rStat.IsDir() {
		remotePath = path.Join(remotePath, filepath.Base(localPath))
	}

	// Ensure remote parent directory exists
	remoteDir := path.Dir(remotePath)
	if err := c.sftpClient.MkdirAll(remoteDir); err != nil {
		return 0, fmt.Errorf("failed to create remote directory %q: %w", remoteDir, err)
	}

	tmpDir, tmpRemote, err := c.createPrivateRemoteTemp(remotePath)
	if err != nil {
		return 0, err
	}

	progressReader := newProgressReader(srcFile, srcStat.Size(), "Uploading", localPath, remotePath)
	n, copyErr := c.copyToRemoteFile(ctx, tmpRemote, progressReader)
	if copyErr != nil {
		_ = c.removeRemoteTemp(tmpDir, tmpRemote)
		return 0, fmt.Errorf("failed to upload data to %q: %w", remotePath, copyErr)
	}

	if err := c.sftpClient.Chmod(tmpRemote, srcStat.Mode().Perm()); err != nil {
		_ = c.removeRemoteTemp(tmpDir, tmpRemote)
		return n, fmt.Errorf("failed to set remote file permissions on %q: %w", tmpRemote, err)
	}

	if err := installRemoteFile(c.sftpClient, tmpRemote, remotePath); err != nil {
		_ = c.removeRemoteTemp(tmpDir, tmpRemote)
		return n, err
	}
	if tmpDir != "" {
		_ = c.sftpClient.RemoveDirectory(tmpDir)
	}
	return n, nil
}

// createPrivateRemoteTemp creates a directory that only the remote account can
// enter, then creates the file through an SSH command under umask 077. SFTP's
// Open/Create packets do not carry a creation mode in this client, so creating
// the file with SFTP and chmod-ing it afterwards would expose the temporary
// upload (and the final file's content) to group/other readers for a window.
func (c *Client) createPrivateRemoteTemp(destPath string) (dir, file string, err error) {
	if c.sshClient == nil {
		// The in-process test server is already private to this process. Real
		// clients always have an SSH connection and use the secure path below.
		file = fmt.Sprintf("%s.tmp.%d", destPath, time.Now().UnixNano())
		dst, createErr := c.sftpClient.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
		if createErr != nil {
			return "", "", fmt.Errorf("failed to create remote temporary file %q: %w", file, createErr)
		}
		if createErr := dst.Chmod(0o600); createErr != nil {
			_ = dst.Close()
			_ = c.sftpClient.Remove(file)
			return "", "", fmt.Errorf("failed to secure remote temporary file %q: %w", file, createErr)
		}
		if createErr := dst.Close(); createErr != nil {
			_ = c.sftpClient.Remove(file)
			return "", "", fmt.Errorf("failed to close remote temporary file %q: %w", file, createErr)
		}
		return "", file, nil
	}

	parent := path.Dir(destPath)
	dir = path.Join(parent, fmt.Sprintf(".opspulse-upload-%d", time.Now().UnixNano()))
	file = path.Join(dir, path.Base(destPath))

	session, err := c.sshClient.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("failed to open remote session for temporary file: %w", err)
	}
	defer func() { _ = session.Close() }()

	script := fmt.Sprintf("umask 077; mkdir -p %s && : > %s",
		shellquote.Quote(dir), shellquote.Quote(file))
	if out, err := session.CombinedOutput(script); err != nil {
		return "", "", fmt.Errorf("failed to create private remote temporary file %q: %w: %s", file, err, strings.TrimSpace(string(out)))
	}
	if err := c.sftpClient.Chmod(dir, 0o700); err != nil {
		_ = c.sftpClient.RemoveDirectory(dir)
		return "", "", fmt.Errorf("failed to secure remote temporary directory %q: %w", dir, err)
	}
	if err := c.sftpClient.Chmod(file, 0o600); err != nil {
		_ = c.sftpClient.Remove(file)
		_ = c.sftpClient.RemoveDirectory(dir)
		return "", "", fmt.Errorf("failed to secure remote temporary file %q: %w", file, err)
	}
	return dir, file, nil
}

// copyToRemoteFile writes through a context-aware reader. Closing the SFTP
// subsystem is the only portable way to interrupt a blocked protocol request;
// the caller closes the whole client as soon as the transfer returns.
func (c *Client) copyToRemoteFile(ctx context.Context, remotePath string, src io.Reader) (int64, error) {
	dst, err := c.sftpClient.OpenFile(remotePath, os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return 0, fmt.Errorf("failed to open remote temporary file %q for writing: %w", remotePath, err)
	}

	type copyResult struct {
		n   int64
		err error
	}
	done := make(chan copyResult, 1)
	go func() {
		n, err := io.Copy(dst, &contextReader{ctx: ctx, r: src})
		done <- copyResult{n: n, err: err}
	}()

	select {
	case <-ctx.Done():
		_ = c.sftpClient.Close()
		// Closing the subsystem unblocks a pending protocol write. Wait for the
		// copy goroutine so it cannot race the cleanup that follows.
		<-done
		return 0, ctx.Err()
	case result := <-done:
		if result.err != nil {
			_ = dst.Close()
			return result.n, result.err
		}
		if err := dst.Close(); err != nil {
			return result.n, fmt.Errorf("failed to close remote temporary file %q: %w", remotePath, err)
		}
		return result.n, nil
	}
}

func (c *Client) removeRemoteTemp(dir, file string) error {
	if c.sftpClient == nil && c.sshClient != nil {
		return c.removeRemoteTempViaSSH(dir, file)
	}

	var errs []error
	if c.sftpClient != nil {
		if err := c.sftpClient.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
		if dir != "" {
			if err := c.sftpClient.RemoveDirectory(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	if len(errs) > 0 && c.sshClient != nil {
		if err := c.removeRemoteTempViaSSH(dir, file); err == nil {
			return nil
		}
	}
	return errors.Join(errs...)
}

// removeRemoteTempViaSSH is the cleanup path after a canceled transfer has
// closed the SFTP subsystem to unblock a write. The SSH connection itself is
// still usable, so the temp directory can be removed without waiting for the
// process to reconnect.
func (c *Client) removeRemoteTempViaSSH(dir, file string) error {
	session, err := c.sshClient.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	script := fmt.Sprintf("rm -f -- %s", shellquote.Quote(file))
	if dir != "" {
		script += fmt.Sprintf("; rmdir -- %s 2>/dev/null || true", shellquote.Quote(dir))
	}
	return session.Run(script)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
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
	return c.DownloadFileContext(context.Background(), remotePath, localPath)
}

// DownloadFileContext downloads a single remote file and aborts on context
// cancellation. A partially written local temporary file is always removed.
func (c *Client) DownloadFileContext(ctx context.Context, remotePath, localPath string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

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
	n, copyErr := copyWithContext(ctx, tmpFile, progressReader)
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

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	type copyResult struct {
		n   int64
		err error
	}
	done := make(chan copyResult, 1)
	go func() {
		n, err := io.Copy(dst, &contextReader{ctx: ctx, r: src})
		done <- copyResult{n: n, err: err}
	}()

	select {
	case <-ctx.Done():
		// The local temp file is removed by DownloadFileContext; the caller's
		// deferred Close also interrupts a blocked SFTP read when the command
		// unwinds.
		<-done
		return 0, ctx.Err()
	case result := <-done:
		return result.n, result.err
	}
}

// localPermFromRemote maps a remote file mode onto the local copy's
// permissions: the owner bits are kept (an executable stays executable) while
// group and other write bits are dropped. The local file is created 0600 by
// os.CreateTemp, so copying a remote 0777 verbatim would only ever widen
// access to the downloaded file.
func localPermFromRemote(mode os.FileMode) os.FileMode {
	return mode.Perm() &^ 0o022
}

// dirWorkers matches internal/cliutil.DefaultParallel (5), while leaving
// capacity for the SSH control channel and other concurrent sessions.
const dirWorkers = 5

type uploadTask struct {
	localPath  string
	remotePath string
}

// UploadDir recursively uploads a local directory to a remote directory.
func (c *Client) UploadDir(localDir, remoteDir string) (int, int64, error) {
	return c.UploadDirContext(context.Background(), localDir, remoteDir)
}

// UploadDirContext recursively uploads a local directory and stops scheduling
// new files once the context is canceled.
func (c *Client) UploadDirContext(ctx context.Context, localDir, remoteDir string) (int, int64, error) {
	localDir = filepath.Clean(localDir)
	info, err := os.Stat(localDir)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to stat local directory %q: %w", localDir, err)
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("local path %q is not a directory", localDir)
	}
	var (
		directories = []string{remoteDir}
		files       []uploadTask
	)
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

		remoteTarget := path.Join(remoteDir, filepath.ToSlash(relPath))
		if currInfo.IsDir() {
			directories = append(directories, remoteTarget)
			return nil
		}
		files = append(files, uploadTask{localPath: currPath, remotePath: remoteTarget})
		return nil
	})
	if err != nil {
		return 0, 0, err
	}

	for _, directory := range directories {
		if err := c.sftpClient.MkdirAll(directory); err != nil {
			return 0, 0, err
		}
	}

	var (
		mu         sync.Mutex
		filesCount int
		totalBytes int64
	)
	err = runBounded(files, dirWorkers, func(task uploadTask) error {
		n, err := c.UploadFileContext(ctx, task.localPath, task.remotePath)
		if err != nil {
			return err
		}
		mu.Lock()
		filesCount++
		totalBytes += n
		mu.Unlock()
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	return filesCount, totalBytes, err
}

type downloadTask struct {
	remotePath string
	localPath  string
}

// DownloadDir recursively downloads a remote directory to a local directory.
func (c *Client) DownloadDir(remoteDir, localDir string) (int, int64, error) {
	return c.DownloadDirContext(context.Background(), remoteDir, localDir)
}

// DownloadDirContext recursively downloads a remote directory and stops
// scheduling new files once the context is canceled.
func (c *Client) DownloadDirContext(ctx context.Context, remoteDir, localDir string) (int, int64, error) {
	remoteDir = path.Clean(remoteDir)
	rStat, err := c.sftpClient.Stat(remoteDir)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to stat remote directory %q: %w", remoteDir, err)
	}
	if !rStat.IsDir() {
		return 0, 0, fmt.Errorf("remote path %q is not a directory", remoteDir)
	}
	var (
		directories = []string{localDir}
		files       []downloadTask
	)
	walker := c.sftpClient.Walk(remoteDir)
	for walker.Step() {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		if walker.Err() != nil {
			return 0, 0, walker.Err()
		}

		currRemote := walker.Path()
		relPath, err := filepath.Rel(filepath.FromSlash(remoteDir), filepath.FromSlash(currRemote))
		if err != nil || relPath == "." {
			continue
		}

		localTarget := filepath.Join(localDir, filepath.FromSlash(relPath))
		if !withinLocalBase(localDir, localTarget) {
			return 0, 0, fmt.Errorf("path traversal detected: remote path %q resolves outside local destination %q", currRemote, localDir)
		}

		stat := walker.Stat()
		if stat.Mode()&os.ModeSymlink != 0 {
			// Skip symlinks to prevent unexpected traversal or dangling links
			continue
		}

		if stat.IsDir() {
			directories = append(directories, localTarget)
			continue
		}
		files = append(files, downloadTask{remotePath: currRemote, localPath: localTarget})
	}

	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return 0, 0, err
		}
	}

	var (
		mu         sync.Mutex
		filesCount int
		totalBytes int64
	)
	err = runBounded(files, dirWorkers, func(task downloadTask) error {
		n, err := c.DownloadFileContext(ctx, task.remotePath, task.localPath)
		if err != nil {
			return err
		}
		mu.Lock()
		filesCount++
		totalBytes += n
		mu.Unlock()
		return nil
	})
	if err == nil {
		err = ctx.Err()
	}
	return filesCount, totalBytes, err
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
