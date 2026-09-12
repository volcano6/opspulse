package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/server"
	"github.com/volcano6/opspulse/internal/sftp"
)

var (
	sftpApp        string
	sftpRemotePath string
	sftpCLI        bool
	sftpListApps   bool
)

var sftpCmd = &cobra.Command{
	Use:   "sftp [server] [flags]",
	Short: "Launch a GUI SFTP client (WinSCP/Xftp/FileZilla) or CLI to manage remote files",
	Long: `Automatically launches a graphical SFTP client connected to the target server.

Supported GUI clients:
  - Windows: WinSCP, Xftp (NetSarang), FileZilla
  - macOS:   Cyberduck, Transmit, FileZilla
  - Linux:   FileZilla, Nautilus, Dolphin, xdg-open

The client process is launched asynchronously in the background so your terminal
remains available immediately.

If no server name is provided, an interactive selector will prompt you to choose one.
To force terminal-based OpenSSH sftp session, pass --cli.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		if sftpListApps {
			return listAvailableSFTPApps()
		}

		store := server.NewDefaultStore()
		var targetServer *server.Server

		if len(args) == 0 {
			servers, err := store.List()
			if err != nil {
				return err
			}
			selected, err := selectServerInteractively(os.Stdin, os.Stdout, servers)
			if err != nil {
				return err
			}
			targetServer = selected
		} else {
			srv, err := store.Get(args[0])
			if err != nil {
				return err
			}
			targetServer = srv
		}

		client, err := sftp.FindClient(sftpApp, sftpCLI)
		if err != nil {
			return fmt.Errorf("resolve SFTP client: %w", err)
		}

		cmd, err := sftp.BuildLaunchCommand(*client, *targetServer, sftpRemotePath)
		if err != nil {
			return fmt.Errorf("build launch command: %w", err)
		}

		if client.IsGUI {
			pathDisplay := sftpRemotePath
			if pathDisplay == "" {
				pathDisplay = "/"
			}
			fmt.Printf("🚀 Launching %s for %s (%s)...\n", client.Name, targetServer.Name, targetServer.Address())
			fmt.Printf("   Remote Path: %s\n", pathDisplay)
			fmt.Printf("   Binary:      %s\n", client.Path)

			if err := sftp.LaunchAsync(cmd); err != nil {
				return fmt.Errorf("failed to launch %s: %w", client.Name, err)
			}
			fmt.Printf("✅ %s launched in background.\n", client.Name)
			return nil
		}

		// CLI interactive OpenSSH mode
		fmt.Printf("--> Connecting to %s (%s) via %s...\n", targetServer.Name, targetServer.Address(), client.Name)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	},
}

func listAvailableSFTPApps() error {
	clients := sftp.DetectAvailableClients()
	if len(clients) == 0 {
		fmt.Println("No SFTP clients detected on this system.")
		fmt.Println("You can install WinSCP, Xftp, or FileZilla, or ensure 'sftp' is in your PATH.")
		return nil
	}

	fmt.Println("Detected SFTP Clients:")
	fmt.Printf("  %-14s %-12s %-6s %s\n", "NAME", "TYPE", "MODE", "PATH")
	fmt.Printf("  %s\n", strings.Repeat("-", 72))

	for _, c := range clients {
		mode := "CLI"
		if c.IsGUI {
			mode = "GUI"
		}
		fmt.Printf("  %-14s %-12s %-6s %s\n", c.Name, c.Type, mode, c.Path)
	}
	return nil
}

func init() {
	sftpCmd.Flags().StringVar(&sftpApp, "app", "", "Explicit GUI SFTP client name (winscp, xftp, filezilla, cyberduck) or executable path")
	sftpCmd.Flags().StringVar(&sftpRemotePath, "path", "/", "Initial remote directory to open")
	sftpCmd.Flags().BoolVar(&sftpCLI, "cli", false, "Use terminal OpenSSH sftp client instead of GUI")
	sftpCmd.Flags().BoolVar(&sftpListApps, "list-apps", false, "List detected SFTP clients on the host system")
	sftpCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(sftpCmd)
}
