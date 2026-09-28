package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/server"
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "导出配置与工具集成",
	Long:  "导出 Ops 配置、清单，以及面向 VS Code 与 Cursor 的 OpenSSH 配置等工具集成。",
}

var (
	exportWritePath string
	exportWrite     bool
	exportFilter    string
)

var exportSSHConfigCmd = &cobra.Command{
	Use:   "ssh-config",
	Short: "把受管服务器导出为 OpenSSH 配置（供 VS Code、Cursor 与原生 ssh 使用）",
	Long: `把受管服务器渲染为 OpenSSH 配置格式，与 VS Code Remote-SSH、Cursor、GoLand
以及原生 'ssh' 终端命令无缝集成。

默认把渲染结果打印到标准输出。
用 --write 自动且幂等地更新 ~/.ssh/config。
--file 只在配合 --write 时生效。

示例：
  # 把 SSH 配置打印到标准输出
  ops export ssh-config

  # 直接写入 ~/.ssh/config（幂等，保留自定义 Host）
  ops export ssh-config --write

  # 把筛选后的服务器写入自定义路径
  ops export ssh-config --write --file ~/.ssh/config.opspulse --filter env=prod`,
	RunE: func(_ *cobra.Command, _ []string) error {
		if exportWritePath != "" && !exportWrite {
			return errors.New("--file requires --write")
		}

		store := server.NewDefaultStore()
		servers, err := store.List()
		if err != nil {
			return fmt.Errorf("list servers: %w", err)
		}

		if exportFilter != "" {
			var filtered []server.Server
			for _, s := range servers {
				if s.MatchFilter(exportFilter) {
					filtered = append(filtered, s)
				}
			}
			servers = filtered
		}

		if !exportWrite {
			rendered := server.RenderSSHConfig(servers)
			_, _ = fmt.Fprint(os.Stdout, rendered)
			return nil
		}

		targetPath := exportWritePath
		if targetPath == "" {
			var pathErr error
			targetPath, pathErr = server.DefaultSSHConfigPath()
			if pathErr != nil {
				return pathErr
			}
		} else {
			targetPath = config.ExpandPath(targetPath)
		}

		if len(servers) == 0 {
			// An empty inventory is not something to report as a success: the
			// block would list no host, and "wrote 0 managed hosts" reads as if
			// the export had done something. A file that never carried the
			// block is left alone, so an export cannot conjure one out of an
			// empty inventory; a file that still carries it is rewritten, which
			// is what clears the hosts left over from a previous export.
			hasBlock, err := hasManagedSSHBlock(targetPath)
			if err != nil {
				return err
			}
			if !hasBlock {
				fmt.Println("No managed hosts configured; nothing to write. Add one with 'ops add <name> <host>'.")
				return nil
			}
			if _, _, err := server.UpdateSSHConfigFile(targetPath, servers); err != nil {
				return fmt.Errorf("write ssh config: %w", err)
			}
			_, _ = fmt.Fprintf(os.Stdout, "Emptied the OpsPulse-managed block in %s (no managed hosts remain).\n", targetPath)
			return nil
		}

		_, count, err := server.UpdateSSHConfigFile(targetPath, servers)
		if err != nil {
			return fmt.Errorf("write ssh config: %w", err)
		}

		_, _ = fmt.Fprintf(os.Stdout, "✅ Successfully wrote %d managed hosts to %s\n", count, targetPath)
		_, _ = fmt.Fprintln(os.Stdout, "   VS Code Remote-SSH, Cursor, and 'ssh <name>' are now ready!")
		return nil
	},
}

// hasManagedSSHBlock reports whether the file at path already carries an
// OpsPulse-managed block.
//
// The markers are the same rule MergeSSHConfigContent applies when it decides
// between replacing an existing block and appending a new one, so a file this
// reports true for is exactly one that export would rewrite.
func hasManagedSSHBlock(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read ssh config %s: %w", path, err)
	}
	content := string(data)
	return strings.Contains(content, server.MarkerBegin) && strings.Contains(content, server.MarkerEnd), nil
}

func init() {
	exportSSHConfigCmd.Flags().BoolVarP(&exportWrite, "write", "w", false, "直接写入 SSH 配置文件（幂等）")
	exportSSHConfigCmd.Flags().StringVar(&exportWritePath, "file", "", "目标 SSH 配置文件路径（默认 ~/.ssh/config；需配合 --write）")
	exportSSHConfigCmd.Flags().StringVarP(&exportFilter, "filter", "f", "", "按 label（key=val）、tag 或名称筛选服务器")

	exportCmd.AddCommand(exportSSHConfigCmd)
	rootCmd.AddCommand(exportCmd)
}
