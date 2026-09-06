package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
)

var (
	cpRecursive bool
	cpTimeout   time.Duration
)

var cpCmd = &cobra.Command{
	Use:   "cp [flags] <source> <destination>",
	Short: "Copy files/directories between local and remote servers via SFTP",
	Long: `Copy files or directories between the local machine and a managed remote server.
The remote location must be prefixed with '<server>:'.

Examples:
  ops cp ./dist vps-1:/var/www/               # Upload to remote server
  ops cp vps-1:/var/log/nginx/access.log .    # Download from remote server
  ops cp -r ./src vps-1:/tmp/src              # Upload directory recursively
  ops cp -r vps-1:/var/log ./logs             # Download directory recursively`,
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

	client, err := sftp.NewClient(*srv, cpTimeout)
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
			filesCount, formatTransferBytes(totalBytes), serverName, remotePath, elapsed.Seconds())
	} else {
		fmt.Printf("--> Uploading file %q ──> %s:%q ...\n", localPath, serverName, remotePath)
		bytesCopied, uploadErr := client.UploadFile(localPath, remotePath)
		if uploadErr != nil {
			return fmt.Errorf("upload failed: %w", uploadErr)
		}
		elapsed := time.Since(startTime)
		fmt.Printf("✅ Uploaded %s to %s:%s in %.2fs\n",
			formatTransferBytes(bytesCopied), serverName, remotePath, elapsed.Seconds())
	}

	return nil
}

func executeDownload(store *server.Store, serverName, remotePath, localPath string) error {
	srv, err := store.Get(serverName)
	if err != nil {
		return err
	}

	client, err := sftp.NewClient(*srv, cpTimeout)
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
			filesCount, formatTransferBytes(totalBytes), serverName, remotePath, localPath, elapsed.Seconds())
	} else {
		fmt.Printf("--> Downloading file %s:%q ──> %q ...\n", serverName, remotePath, localPath)
		bytesCopied, downloadErr := client.DownloadFile(remotePath, localPath)
		if downloadErr != nil {
			return fmt.Errorf("download failed: %w", downloadErr)
		}
		elapsed := time.Since(startTime)
		fmt.Printf("✅ Downloaded %s from %s:%s to %s in %.2fs\n",
			formatTransferBytes(bytesCopied), serverName, remotePath, localPath, elapsed.Seconds())
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

func formatTransferBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB", "EB"}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < len(units)-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %s", float64(b)/float64(div), units[exp])
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
	cpCmd.Flags().BoolVarP(&cpRecursive, "recursive", "r", false, "Copy directory recursively")
	cpCmd.Flags().DurationVarP(&cpTimeout, "timeout", "T", 60*time.Second, "SFTP connection timeout")
	cpCmd.ValidArgsFunction = completeCpArgs

	rootCmd.AddCommand(cpCmd)
}
