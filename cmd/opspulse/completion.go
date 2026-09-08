package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

var completionInstall bool

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion script or install automatically",
	Long: `Generate shell autocompletion scripts for Ops (ops) or install them
into your shell configuration files automatically using the --install flag.

Supported shells: bash, zsh, fish, powershell.

Examples:
  # Install autocompletion directly into your shell profile
  ops completion --install

  # Generate completion script to stdout
  ops completion bash
  ops completion zsh
  ops completion fish
  ops completion powershell`,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	Args:                  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		shellType := ""
		if len(args) > 0 {
			shellType = strings.ToLower(args[0])
		} else if completionInstall {
			shellType = detectCurrentShell()
		} else {
			return cmd.Help()
		}

		if completionInstall {
			return installCompletion(cmd.Root(), shellType)
		}

		return generateCompletion(cmd.Root(), shellType, os.Stdout)
	},
}

func init() {
	completionCmd.Flags().BoolVarP(&completionInstall, "install", "i", false, "Install completion script into current user's shell profile automatically")
	rootCmd.AddCommand(completionCmd)
}

func detectCurrentShell() string {
	shellEnv := os.Getenv("SHELL")
	if shellEnv != "" {
		base := strings.ToLower(filepath.Base(shellEnv))
		switch {
		case strings.Contains(base, "zsh"):
			return "zsh"
		case strings.Contains(base, "bash"):
			return "bash"
		case strings.Contains(base, "fish"):
			return "fish"
		}
	}
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	return "bash"
}

func generateCompletion(root *cobra.Command, shell string, out io.Writer) error {
	switch shell {
	case "bash":
		return root.GenBashCompletionV2(out, true)
	case "zsh":
		return root.GenZshCompletion(out)
	case "fish":
		return root.GenFishCompletion(out, true)
	case "powershell":
		return root.GenPowerShellCompletionWithDesc(out)
	default:
		return fmt.Errorf("unsupported shell %q (supported: bash, zsh, fish, powershell)", shell)
	}
}

func installCompletion(root *cobra.Command, shell string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home directory: %w", err)
	}

	ensureBinaryInPath(home)

	const beginMarker = "# >>> Ops Completion >>>"
	const endMarker = "# <<< Ops Completion <<<"

	switch shell {
	case "bash":
		targetFile := filepath.Join(home, ".bashrc")
		content := fmt.Sprintf("%s\nif [ -d \"$HOME/.local/bin\" ]; then\n  case \":$PATH:\" in\n    *\":$HOME/.local/bin:\"*) ;;\n    *) export PATH=\"$HOME/.local/bin:$PATH\" ;;\n  esac\nfi\nif [ -d \"$HOME/go/bin\" ]; then\n  case \":$PATH:\" in\n    *\":$HOME/go/bin:\"*) ;;\n    *) export PATH=\"$HOME/go/bin:$PATH\" ;;\n  esac\nfi\nif command -v ops >/dev/null 2>&1; then\n  eval \"$(ops completion bash)\"\nfi\n%s\n", beginMarker, endMarker)
		return updateProfileFile(targetFile, beginMarker, endMarker, content)

	case "zsh":
		targetFile := filepath.Join(home, ".zshrc")
		content := fmt.Sprintf("%s\nif [ -d \"$HOME/.local/bin\" ]; then\n  case \":$PATH:\" in\n    *\":$HOME/.local/bin:\"*) ;;\n    *) export PATH=\"$HOME/.local/bin:$PATH\" ;;\n  esac\nfi\nif [ -d \"$HOME/go/bin\" ]; then\n  case \":$PATH:\" in\n    *\":$HOME/go/bin:\"*) ;;\n    *) export PATH=\"$HOME/go/bin:$PATH\" ;;\n  esac\nfi\nif command -v ops >/dev/null 2>&1; then\n  eval \"$(ops completion zsh)\"\nfi\n%s\n", beginMarker, endMarker)
		return updateProfileFile(targetFile, beginMarker, endMarker, content)

	case "fish":
		targetDir := filepath.Join(home, ".config", "fish", "completions")
		if err := os.MkdirAll(targetDir, 0o750); err != nil {
			return fmt.Errorf("create fish completions dir: %w", err)
		}
		targetFile := filepath.Clean(filepath.Join(targetDir, "ops.fish"))
		var buf bytes.Buffer
		if err := root.GenFishCompletion(&buf, true); err != nil {
			return fmt.Errorf("generate fish completion: %w", err)
		}
		if err := os.WriteFile(targetFile, buf.Bytes(), 0o600); err != nil { // #nosec G703,G304 -- fish completion file in user home directory
			return fmt.Errorf("write fish completion: %w", err)
		}
		targetOpspulseFile := filepath.Clean(filepath.Join(targetDir, "opspulse.fish"))
		_ = os.WriteFile(targetOpspulseFile, buf.Bytes(), 0o600) // #nosec G703,G304
		fmt.Printf("✅ Fish completions written to %s\n", targetFile)
		return nil

	case "powershell":
		targetDir := filepath.Join(home, "Documents", "PowerShell")
		if runtime.GOOS != "windows" {
			targetDir = filepath.Join(home, ".config", "powershell")
		}
		_ = os.MkdirAll(targetDir, 0o750)
		targetFile := filepath.Join(targetDir, "Microsoft.PowerShell_profile.ps1")
		content := fmt.Sprintf("%s\n$gopathBin = Join-Path $HOME \"go\\bin\"\nif (Test-Path $gopathBin) {\n    if ($env:PATH -notlike \"*$gopathBin*\") { $env:PATH = \"$gopathBin;$env:PATH\" }\n}\n$localBin = Join-Path $HOME \".local\\bin\"\nif (Test-Path $localBin) {\n    if ($env:PATH -notlike \"*$localBin*\") { $env:PATH = \"$localBin;$env:PATH\" }\n}\nif (Get-Command ops -ErrorAction SilentlyContinue) {\n    Invoke-Expression (&ops completion powershell | Out-String)\n}\n%s\n", beginMarker, endMarker)
		return updateProfileFile(targetFile, beginMarker, endMarker, content)

	default:
		return fmt.Errorf("unsupported shell %q for auto-installation", shell)
	}
}

func updateProfileFile(path, beginMarker, endMarker, blockContent string) error {
	cleanPath := filepath.Clean(path)
	var existingContent string
	if data, err := os.ReadFile(cleanPath); err == nil { // #nosec G703,G304 -- reading user profile configuration file
		existingContent = string(data)
	}

	newContent := blockContent
	if existingContent != "" {
		startIdx := strings.Index(existingContent, beginMarker)
		endIdx := strings.Index(existingContent, endMarker)
		if startIdx != -1 && endIdx != -1 && endIdx >= startIdx {
			endIdx += len(endMarker)
			if endIdx < len(existingContent) && existingContent[endIdx] == '\n' {
				endIdx++
			}
			newContent = existingContent[:startIdx] + blockContent + existingContent[endIdx:]
		} else {
			if !strings.HasSuffix(existingContent, "\n") {
				existingContent += "\n"
			}
			newContent = existingContent + "\n" + blockContent
		}
	}

	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	if err := os.WriteFile(cleanPath, []byte(newContent), 0o600); err != nil { // #nosec G703,G304 -- writing user profile configuration file
		return fmt.Errorf("write configuration to %s: %w", cleanPath, err)
	}

	fmt.Printf("✅ Shell completion installed into %s\n", path)
	fmt.Printf("   Please reload your shell (e.g. source %s) to enable autocompletion.\n", path)
	return nil
}

func ensureBinaryInPath(home string) {
	if _, err := exec.LookPath("ops"); err == nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	localBin := filepath.Clean(filepath.Join(home, ".local", "bin"))
	if err := os.MkdirAll(localBin, 0o750); err != nil {
		return
	}
	targetExe := filepath.Clean(filepath.Join(localBin, "ops"))
	data, err := os.ReadFile(filepath.Clean(exe))
	if err != nil {
		return
	}
	// #nosec G703
	if err := os.WriteFile(targetExe, data, 0o600); err != nil {
		return
	}
	// #nosec G302
	if err := os.Chmod(targetExe, 0o755); err == nil {
		fmt.Printf("✅ Automatically installed 'ops' binary to %s (user PATH)\n", targetExe)
	}
	targetOpspulse := filepath.Clean(filepath.Join(localBin, "opspulse"))
	// #nosec G703
	if err := os.WriteFile(targetOpspulse, data, 0o600); err == nil {
		// #nosec G302
		_ = os.Chmod(targetOpspulse, 0o755)
	}
}
