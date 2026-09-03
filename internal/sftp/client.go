// Package sftp provides high-performance SFTP file and directory transfer capabilities.
package sftp

import (
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
}

// NewClient establishes an SSH connection and initializes an SFTP subsystem client.
func NewClient(srv server.Server, timeout time.Duration) (*Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	config, err := executor.BuildClientConfig(srv, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to build ssh config for %s: %w", srv.Name, err)
	}

	sshConn, err := ssh.Dial("tcp", srv.Address(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s (%s): %w", srv.Name, srv.Address(), err)
	}

	sftpConn, err := sftp.NewClient(sshConn)
	if err != nil {
		_ = sshConn.Close()
		return nil, fmt.Errorf("failed to initialize sftp subsystem on %s: %w", srv.Name, err)
	}

	return &Client{
		sshClient:  sshConn,
		sftpClient: sftpConn,
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
	if c.sshClient != nil {
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

	dstFile, err := c.sftpClient.Create(remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to create remote file %q: %w", remotePath, err)
	}
	defer func() { _ = dstFile.Close() }()

	n, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return 0, fmt.Errorf("failed to upload data to %q: %w", remotePath, err)
	}

	if err := c.sftpClient.Chmod(remotePath, srcStat.Mode().Perm()); err != nil {
		return n, fmt.Errorf("failed to set remote file permissions on %q: %w", remotePath, err)
	}
	return n, nil
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

	dstFile, err := os.Create(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create local file %q: %w", localPath, err)
	}
	defer func() { _ = dstFile.Close() }()

	n, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return 0, fmt.Errorf("failed to download data to %q: %w", localPath, err)
	}

	if err := os.Chmod(localPath, srcStat.Mode().Perm()); err != nil {
		return n, fmt.Errorf("failed to set local file permissions on %q: %w", localPath, err)
	}
	return n, nil
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
		stat := walker.Stat()

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
