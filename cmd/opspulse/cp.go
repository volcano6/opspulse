package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/format"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
)

var (
	cpRecursive bool
	cpTimeout   time.Duration
)

// newSFTPClient is the SFTP client constructor both transfer directions use.
// It is a variable so tests can observe the server and jump host that were
// resolved without opening an SSH connection.
var newSFTPClient = sftp.NewClientWithJumpAndWriter

// resolveJumpServer resolves srv's configured jump host against the inventory.
//
// It mirrors executor.DialTarget's resolution: an empty JumpHost means a direct
// connection, while a name that is not in the inventory is a hard error. The
// alternative — falling back to the target's own address — would either time
// out on an unroutable private address or, worse, silently bypass the jump host
// whenever that address happens to be reachable.
func resolveJumpServer(store *server.Store, srv *server.Server) (*server.Server, error) {
	if srv.JumpHost == "" {
		return nil, nil
	}
	jump, err := store.Get(srv.JumpHost)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve jump host %q for server %q: %w", srv.JumpHost, srv.Name, err)
	}
	return jump, nil
}

var cpCmd = &cobra.Command{
	Use:   "cp [flags] <source> <destination>",
	Short: "经 SFTP 在本地与远端服务器之间拷贝文件或目录",
	Long: `在本地与受管远端服务器之间拷贝文件或目录。
远端位置必须以 '<server>:' 前缀标识。

示例：
  ops cp ./dist vps-1:/var/www/               # 上传到远端服务器
  ops cp vps-1:/var/log/nginx/access.log .    # 从远端服务器下载
  ops cp -r ./src vps-1:/tmp/src              # 递归上传目录
  ops cp -r vps-1:/var/log ./logs             # 递归下载目录`,
	Args: cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		src := args[0]
		dst := args[1]

		srcServer, srcPath, srcIsRemote := parseRemotePath(src)
		dstServer, dstPath, dstIsRemote := parseRemotePath(dst)

		if srcIsRemote && dstIsRemote {
			return errors.New("cross-server direct copy is not supported; transfer via local intermediate or use ops backup/restore")
		}
		if !srcIsRemote && !dstIsRemote {
			return errors.New("one of source or destination must be a remote server path formatted as <server>:<path>")
		}

		store := server.NewDefaultStore()

		if dstIsRemote {
			// Upload mode: local -> remote
			return executeUpload(store, dstServer, srcPath, dstPath)
		}

		// Download mode: remote -> local
		return executeDownload(store, srcServer, srcPath, dstPath)
	},
}

func executeUpload(store *server.Store, serverName, localPath, remotePath string) error {
	srv, err := store.Get(serverName)
	if err != nil {
		return err
	}

	lStat, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("local path error: %w", err)
	}

	if lStat.IsDir() && !cpRecursive {
		return fmt.Errorf("%q is a directory. Use --recursive (-r) to upload directories", localPath)
	}

	jump, err := resolveJumpServer(store, srv)
	if err != nil {
		return err
	}

	client, err := newSFTPClient(*srv, jump, cpTimeout, os.Stderr)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	startTime := time.Now()
	if lStat.IsDir() {
		fmt.Printf("--> Uploading directory %q ──> %s:%q ...\n", localPath, serverName, remotePath)
		filesCount, totalBytes, uploadErr := client.UploadDir(localPath, remotePath)
		if uploadErr != nil {
			return fmt.Errorf("upload failed: %w", uploadErr)
		}
		elapsed := time.Since(startTime)
		fmt.Printf("✅ Uploaded %d files (%s) to %s:%s in %.2fs\n",
			filesCount, format.Bytes(totalBytes), serverName, remotePath, elapsed.Seconds())
	} else {
		fmt.Printf("--> Uploading file %q ──> %s:%q ...\n", localPath, serverName, remotePath)
		bytesCopied, uploadErr := client.UploadFile(localPath, remotePath)
		if uploadErr != nil {
			return fmt.Errorf("upload failed: %w", uploadErr)
		}
		elapsed := time.Since(startTime)
		fmt.Printf("✅ Uploaded %s to %s:%s in %.2fs\n",
			format.Bytes(bytesCopied), serverName, remotePath, elapsed.Seconds())
	}

	return nil
}

func executeDownload(store *server.Store, serverName, remotePath, localPath string) error {
	srv, err := store.Get(serverName)
	if err != nil {
		return err
	}

	jump, err := resolveJumpServer(store, srv)
	if err != nil {
		return err
	}

	client, err := newSFTPClient(*srv, jump, cpTimeout, os.Stderr)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	startTime := time.Now()
	if cpRecursive {
		fmt.Printf("--> Downloading directory %s:%q ──> %q ...\n", serverName, remotePath, localPath)
		filesCount, totalBytes, downloadErr := client.DownloadDir(remotePath, localPath)
		if downloadErr != nil {
			return fmt.Errorf("download failed: %w", downloadErr)
		}
		elapsed := time.Since(startTime)
		fmt.Printf("✅ Downloaded %d files (%s) from %s:%s to %s in %.2fs\n",
			filesCount, format.Bytes(totalBytes), serverName, remotePath, localPath, elapsed.Seconds())
	} else {
		fmt.Printf("--> Downloading file %s:%q ──> %q ...\n", serverName, remotePath, localPath)
		bytesCopied, downloadErr := client.DownloadFile(remotePath, localPath)
		if downloadErr != nil {
			return fmt.Errorf("download failed: %w", downloadErr)
		}
		elapsed := time.Since(startTime)
		fmt.Printf("✅ Downloaded %s from %s:%s to %s in %.2fs\n",
			format.Bytes(bytesCopied), serverName, remotePath, localPath, elapsed.Seconds())
	}

	return nil
}

func parseRemotePath(target string) (serverName, remotePath string, isRemote bool) {
	// Exclude Windows drive letter (e.g. C:\foo or C:/foo)
	if len(target) >= 2 && ((target[0] >= 'a' && target[0] <= 'z') || (target[0] >= 'A' && target[0] <= 'Z')) && target[1] == ':' {
		if len(target) == 2 || target[2] == '\\' || target[2] == '/' {
			return "", target, false
		}
	}
	idx := strings.Index(target, ":")
	if idx <= 0 {
		return "", target, false
	}
	return target[:idx], target[idx+1:], true
}

func completeCpArgs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) >= 2 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// Complete server names with a trailing colon
	store := server.NewDefaultStore()
	servers, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	var completions []string
	for _, s := range servers {
		prefix := s.Name + ":"
		if strings.HasPrefix(prefix, toComplete) {
			desc := s.Host
			if s.Description != "" {
				desc = fmt.Sprintf("%s (%s)", s.Host, s.Description)
			}
			completions = append(completions, fmt.Sprintf("%s\t%s", prefix, desc))
		}
	}
	return completions, cobra.ShellCompDirectiveDefault
}

func init() {
	cpCmd.Flags().BoolVarP(&cpRecursive, "recursive", "r", false, "递归拷贝目录")
	cpCmd.Flags().DurationVarP(&cpTimeout, "timeout", "T", 60*time.Second, "SFTP 连接超时")
	cpCmd.ValidArgsFunction = completeCpArgs

	rootCmd.AddCommand(cpCmd)
}
