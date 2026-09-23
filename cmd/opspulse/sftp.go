package main

import (
	"fmt"
	"os"
	"os/exec"
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
	sftpCleanup    bool
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
		if sftpCleanup {
			// Kept working so an existing script is not broken outright, but no
			// longer advertised: nothing writes to ~/.ssh/opspulse-1p any more,
			// and 'ops 1p restore' purges whatever the op:// era left behind.
			fmt.Fprintln(os.Stderr, "⚠️  --cleanup is deprecated: OpsPulse no longer materialises temporary keys in ~/.ssh/opspulse-1p, and 'ops 1p restore' purges leftovers from the op:// era automatically. This flag will be removed in a future release.")

			targetServer := ""
			if len(args) > 0 {
				targetServer = args[0]
			}
			deleted, err := sftp.PurgeMaterialized1PKeys(targetServer)
			if err != nil {
				return fmt.Errorf("failed to clean up materialized keys: %w", err)
			}
			if len(deleted) == 0 {
				if targetServer != "" {
					fmt.Printf("✨ No materialized key found for server %q in ~/.ssh/opspulse-1p.\n", targetServer)
				} else {
					fmt.Println("✨ No materialized 1Password keys found on disk (~/.ssh/opspulse-1p is clean).")
				}
				return nil
			}
			if targetServer != "" {
				fmt.Printf("🧹 Successfully removed materialized 1Password key for %q.\n", targetServer)
			} else {
				fmt.Printf("🧹 Successfully removed %d materialized 1Password private key(s) from ~/.ssh/opspulse-1p: %s\n",
					len(deleted), strings.Join(deleted, ", "))
			}
			return nil
		}

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
			if details, err := sftp.ListMaterialized1PKeyDetails(); err == nil {
				for _, d := range details {
					if d.IsStale {
						fmt.Printf("   ⚠️  Stale key on disk: %s (>24h old; 'ops 1p restore' purges these leftovers automatically)\n", d.Name)
					}
				}
			}
			return nil
		}

		// CLI interactive OpenSSH mode
		fmt.Printf("--> Connecting to %s (%s) via %s...\n", targetServer.Name, targetServer.Address(), client.Name)

		// Same multiplexing directory the ssh command uses, so the two share
		// one authenticated socket instead of authenticating twice.
		prepareControlMaster()

		cleanupAskpass, err := attachAskpass(cmd, *targetServer)
		if err != nil {
			return err
		}
		defer cleanupAskpass()

		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	},
}

// attachAskpass wires a child sftp process to this binary as its SSH_ASKPASS
// helper, so a password-auth server connects without the user retyping a
// password the inventory already holds.
//
// A key-only server needs nothing: it returns a no-op cleanup so the caller can
// defer unconditionally.
func attachAskpass(cmd *exec.Cmd, srv server.Server) (func(), error) {
	noop := func() {}
	if srv.Password == "" {
		return noop, nil
	}

	selfPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve SSH password helper: %w", err)
	}

	passwordPath, cleanup, err := newAskpassFile(buildAskpassConfig(srv, nil, srv.Password, ""))
	if err != nil {
		return nil, err
	}
	cmd.Env = overrideEnv(os.Environ(), askpassEnv(selfPath, passwordPath))
	return cleanup, nil
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
	sftpCmd.Flags().BoolVar(&sftpCleanup, "cleanup", false, "deprecated: 'ops 1p restore' purges these leftovers automatically")
	_ = sftpCmd.Flags().MarkHidden("cleanup")
	sftpCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(sftpCmd)
}
