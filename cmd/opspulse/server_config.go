package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/server"
)

var (
	setHost string
	setPort int
	setKey  string
)

var serverSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Update selected fields of an existing server",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var host *string
		var port *int
		var key *string
		if cmd.Flags().Changed("host") {
			host = &setHost
		}
		if cmd.Flags().Changed("port") {
			port = &setPort
		}
		if cmd.Flags().Changed("key") {
			key = &setKey
		}
		return setServerFields(server.NewDefaultStore(), args[0], host, port, key)
	},
}

func setServerFields(store *server.Store, name string, host *string, port *int, key *string) error {
	srv, err := store.Get(name)
	if err != nil {
		return err
	}
	if host == nil && port == nil && key == nil {
		return fmt.Errorf("at least one of --host, --port, or --key is required")
	}
	if host != nil {
		if strings.TrimSpace(*host) == "" {
			return fmt.Errorf("--host cannot be empty")
		}
		srv.Host = *host
	}
	if port != nil {
		if *port <= 0 || *port > 65535 {
			return fmt.Errorf("--port must be between 1 and 65535")
		}
		srv.Port = *port
	}
	if key != nil {
		srv.KeyPath = *key
	}
	if err := store.Save(*srv); err != nil {
		return fmt.Errorf("update server %q: %w", srv.Name, err)
	}
	fmt.Printf("Server %q updated successfully.\n", srv.Name)
	return nil
}

var serverEditCmd = &cobra.Command{
	Use:   "edit <name>",
	Short: "Edit the server inventory and validate it before saving",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return editServerConfig(server.NewDefaultStore(), args[0])
	},
}

func editServerConfig(store *server.Store, serverName string) error {
	if _, err := store.Get(serverName); err != nil {
		return err
	}
	original, err := os.ReadFile(store.FilePath())
	if err != nil {
		return fmt.Errorf("read server configuration: %w", err)
	}
	line := serverYAMLLine(original, serverName)

	draft, err := os.CreateTemp(filepath.Dir(store.FilePath()), ".servers-*.yaml")
	if err != nil {
		return fmt.Errorf("create server configuration draft: %w", err)
	}
	draftPath := draft.Name()
	defer func() { _ = os.Remove(draftPath) }()
	if err := draft.Chmod(0o600); err != nil {
		_ = draft.Close()
		return fmt.Errorf("secure server configuration draft: %w", err)
	}
	if _, err := draft.Write(original); err != nil {
		_ = draft.Close()
		return fmt.Errorf("write server configuration draft: %w", err)
	}
	if err := draft.Close(); err != nil {
		return fmt.Errorf("close server configuration draft: %w", err)
	}

	if err := openEditor(draftPath, line); err != nil {
		return err
	}
	updated, err := os.ReadFile(draftPath)
	if err != nil {
		return fmt.Errorf("read edited server configuration: %w", err)
	}
	if err := store.Validate(updated); err != nil {
		return fmt.Errorf("edited configuration is invalid; original file was not changed: %w", err)
	}
	draftStore := server.NewStore(draftPath)
	if _, err := draftStore.Get(serverName); err != nil {
		return fmt.Errorf("edited configuration must retain server %q; original file was not changed", serverName)
	}
	if err := store.Replace(updated); err != nil {
		return fmt.Errorf("save edited configuration: %w", err)
	}
	fmt.Printf("Server configuration validated and saved to %s\n", store.FilePath())
	return nil
}

func openEditor(path string, line int) error {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "vi"
		}
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return fmt.Errorf("editor command is empty")
	}
	args := append([]string(nil), parts[1:]...)
	switch filepath.Base(parts[0]) {
	case "vi", "vim", "nvim", "view":
		args = append(args, "+"+strconv.Itoa(line), path)
	case "nano":
		args = append(args, "+"+strconv.Itoa(line)+",1", path)
	case "code", "code-insiders":
		args = append(args, "--wait", "--goto", fmt.Sprintf("%s:%d:1", path, line))
	default:
		args = append(args, path)
	}
	cmd := exec.Command(parts[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q failed: %w", parts[0], err)
	}
	return nil
}

func serverYAMLLine(data []byte, serverName string) int {
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "- name: "+serverName || trimmed == "name: "+serverName ||
			trimmed == `- name: "`+serverName+`"` || trimmed == `name: "`+serverName+`"` {
			return i + 1
		}
	}
	return 1
}

func init() {
	serverSetCmd.Flags().StringVar(&setHost, "host", "", "New server IP or hostname")
	serverSetCmd.Flags().IntVarP(&setPort, "port", "p", 0, "New SSH port")
	serverSetCmd.Flags().StringVarP(&setKey, "key", "k", "", "New private key path (empty clears it)")
	_ = serverSetCmd.RegisterFlagCompletionFunc("key", completePrivateKeyPath)
	serverSetCmd.ValidArgsFunction = completeServerNames
	serverEditCmd.ValidArgsFunction = completeServerNames
	serverCmd.AddCommand(serverSetCmd, serverEditCmd)
}
