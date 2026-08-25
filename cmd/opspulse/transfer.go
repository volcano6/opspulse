package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
)

var (
	uploadRecursive   bool
	downloadRecursive bool
	transferTimeout   time.Duration
)

var uploadCmd = &cobra.Command{
	Use:   "upload <server> <local-path> <remote-path>",
	Short: "Upload a file or directory to a remote server via SFTP",
	Long: `Upload local files or directories to a remote server using the SFTP protocol.
Automatically resolves SSH connection parameters from servers.yaml.`,
	Args: cobra.ExactArgs(3),
	RunE: func(_ *cobra.Command, args []string) error {
		serverName := args[0]
		localPath := args[1]
		remotePath := args[2]

		store := server.NewDefaultStore()
		srv, err := store.Get(serverName)
		if err != nil {
			return err
		}

		lStat, err := os.Stat(localPath)
		if err != nil {
			return fmt.Errorf("local path error: %w", err)
		}

		if lStat.IsDir() && !uploadRecursive {
			return fmt.Errorf("%q is a directory. Use --recursive (-r) to upload directories", localPath)
		}

		client, err := sftp.NewClient(*srv, transferTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		startTime := time.Now()
		if lStat.IsDir() {
			fmt.Printf("--> Uploading directory %q ──> %s:%q ...\n", localPath, serverName, remotePath)
			filesCount, totalBytes, err := client.UploadDir(localPath, remotePath)
			if err != nil {
				return fmt.Errorf("upload failed: %w", err)
			}
			elapsed := time.Since(startTime)
			fmt.Printf("✅ Uploaded %d files (%s) to %s:%s in %.2fs\n",
				filesCount, formatTransferBytes(totalBytes), serverName, remotePath, elapsed.Seconds())
		} else {
			fmt.Printf("--> Uploading file %q ──> %s:%q ...\n", localPath, serverName, remotePath)
			bytesCopied, err := client.UploadFile(localPath, remotePath)
			if err != nil {
				return fmt.Errorf("upload failed: %w", err)
			}
			elapsed := time.Since(startTime)
			fmt.Printf("✅ Uploaded %s to %s:%s in %.2fs\n",
				formatTransferBytes(bytesCopied), serverName, remotePath, elapsed.Seconds())
		}

		return nil
	},
}

var downloadCmd = &cobra.Command{
	Use:   "download <server> <remote-path> <local-path>",
	Short: "Download a file or directory from a remote server via SFTP",
	Long: `Download remote files or directories to the local machine using the SFTP protocol.
Automatically resolves SSH connection parameters from servers.yaml.`,
	Args: cobra.ExactArgs(3),
	RunE: func(_ *cobra.Command, args []string) error {
		serverName := args[0]
		remotePath := args[1]
		localPath := args[2]

		store := server.NewDefaultStore()
		srv, err := store.Get(serverName)
		if err != nil {
			return err
		}

		client, err := sftp.NewClient(*srv, transferTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		startTime := time.Now()
		if downloadRecursive {
			fmt.Printf("--> Downloading directory %s:%q ──> %q ...\n", serverName, remotePath, localPath)
			filesCount, totalBytes, err := client.DownloadDir(remotePath, localPath)
			if err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			elapsed := time.Since(startTime)
			fmt.Printf("✅ Downloaded %d files (%s) from %s:%s to %s in %.2fs\n",
				filesCount, formatTransferBytes(totalBytes), serverName, remotePath, localPath, elapsed.Seconds())
		} else {
			fmt.Printf("--> Downloading file %s:%q ──> %q ...\n", serverName, remotePath, localPath)
			bytesCopied, err := client.DownloadFile(remotePath, localPath)
			if err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			elapsed := time.Since(startTime)
			fmt.Printf("✅ Downloaded %s from %s:%s to %s in %.2fs\n",
				formatTransferBytes(bytesCopied), serverName, remotePath, localPath, elapsed.Seconds())
		}

		return nil
	},
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

func init() {
	uploadCmd.Flags().BoolVarP(&uploadRecursive, "recursive", "r", false, "Upload directory recursively")
	uploadCmd.Flags().DurationVarP(&transferTimeout, "timeout", "T", 60*time.Second, "SFTP connection timeout")
	uploadCmd.ValidArgsFunction = completeServerNames

	downloadCmd.Flags().BoolVarP(&downloadRecursive, "recursive", "r", false, "Download directory recursively")
	downloadCmd.Flags().DurationVarP(&transferTimeout, "timeout", "T", 60*time.Second, "SFTP connection timeout")
	downloadCmd.ValidArgsFunction = completeServerNames

	rootCmd.AddCommand(uploadCmd)
	rootCmd.AddCommand(downloadCmd)
}
