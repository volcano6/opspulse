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
)

var sftpCmd = &cobra.Command{
	Use:   "sftp [server] [flags]",
	Short: "唤起 GUI SFTP 客户端（WinSCP/Xftp/FileZilla）或 CLI 管理远端文件",
	Long: `自动唤起连接到目标服务器的图形化 SFTP 客户端。

支持的 GUI 客户端：
  - Windows：WinSCP、Xftp (NetSarang)、FileZilla
  - macOS：  Cyberduck、Transmit、FileZilla
  - Linux：  FileZilla、Nautilus、xdg-open

客户端以异步独立进程在后台拉起，终端立即返回可用。

未提供服务器名时，会弹出交互式选择菜单供你挑选。
需要强制使用终端原生 OpenSSH sftp 会话时，传入 --cli。`,
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

		// Resolve the jump host once. A GUI client is refused outright by
		// BuildLaunchCommand, and the terminal client tunnels through it.
		var jumpSrv *server.Server
		if targetServer.JumpHost != "" {
			if jumpSrv, err = store.Get(targetServer.JumpHost); err != nil {
				return fmt.Errorf("failed to resolve jump host %q for server %q: %w", targetServer.JumpHost, targetServer.Name, err)
			}
		}

		cmd, err := sftp.BuildLaunchCommand(*client, *targetServer, jumpSrv, sftpRemotePath)
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
		if jumpSrv != nil {
			fmt.Printf("--> Connecting to %s (%s) via %s through jump host %s...\n", targetServer.Name, targetServer.Address(), client.Name, jumpSrv.Name)
		} else {
			fmt.Printf("--> Connecting to %s (%s) via %s...\n", targetServer.Name, targetServer.Address(), client.Name)
		}

		// Same multiplexing directory the ssh command uses, so the two share
		// one authenticated socket instead of authenticating twice.
		prepareControlMaster()

		cleanupAskpass, err := attachAskpass(cmd, *targetServer, jumpSrv)
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
// password the inventory already holds. jumpSrv, when non-nil, is added to the
// payload so the ssh(1) process the ProxyCommand spawns can answer the jump
// host's prompt from the inventory as well.
//
// A key-only server with a key-only jump host needs nothing: it returns a no-op
// cleanup so the caller can defer unconditionally.
func attachAskpass(cmd *exec.Cmd, srv server.Server, jumpSrv *server.Server) (func(), error) {
	noop := func() {}
	var jumpPassword string
	if jumpSrv != nil {
		jumpPassword = jumpSrv.Password
	}
	if srv.Password == "" && jumpPassword == "" {
		return noop, nil
	}

	selfPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve SSH password helper: %w", err)
	}

	passwordPath, cleanup, err := newAskpassFile(buildAskpassConfig(srv, jumpSrv, srv.Password, jumpPassword))
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
	sftpCmd.Flags().StringVar(&sftpApp, "app", "", "显式指定 GUI SFTP 客户端名称（winscp、xftp、filezilla、cyberduck）或可执行文件路径")
	sftpCmd.Flags().StringVar(&sftpRemotePath, "path", "/", "打开的远端初始目录")
	sftpCmd.Flags().BoolVar(&sftpCLI, "cli", false, "使用终端原生 OpenSSH sftp 客户端而非 GUI")
	sftpCmd.Flags().BoolVar(&sftpListApps, "list-apps", false, "列出本机检测到的 SFTP 客户端")
	sftpCmd.ValidArgsFunction = completeServerNames
	rootCmd.AddCommand(sftpCmd)
}
