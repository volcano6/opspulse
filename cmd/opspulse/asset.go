package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/asset"
	"github.com/volcano6/opspulse/internal/backup"
)

var assetCmd = &cobra.Command{
	Use:   "asset",
	Short: "管理业务资产",
	Long: `定义、查看并管理有状态的业务资产（Docker Compose 项目、Volume、数据库、目录、文件），
每个资产一个稳定 ID，备份与跨机还原时按 ID 引用。

资产记录保存在 $XDG_CONFIG_HOME/opspulse/assets.yaml。`,
}

var assetListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出全部已配置资产",
	RunE: func(_ *cobra.Command, _ []string) error {
		store := asset.NewDefaultStore()
		assets, err := store.List()
		if err != nil {
			return fmt.Errorf("failed to list assets: %w", err)
		}

		if len(assets) == 0 {
			fmt.Printf("No assets configured. Add assets via:\n  ops asset add <id> --type <type> --source <path>\n\nConfig: %s\n", store.FilePath())
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tTYPE\tSOURCE\tENGINE\tDESCRIPTION")
		_, _ = fmt.Fprintln(tw, "--\t----\t------\t------\t-----------")

		for _, a := range assets {
			engine := "-"
			if a.Engine != "" {
				engine = a.Engine
			}
			desc := "-"
			if a.Description != "" {
				desc = a.Description
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				a.ID, string(a.Type), a.Source, engine, desc)
		}

		return tw.Flush()
	},
}

var (
	assetAddType      string
	assetAddSource    string
	assetAddEngine    string
	assetAddContainer string
	assetAddExcludes  string
	assetAddDesc      string
)

var assetAddCmd = &cobra.Command{
	Use:   "add <id>",
	Short: "注册或更新业务资产",
	Long: `注册一个有状态的业务资产，并分配一个稳定 ID，供备份与还原按 ID 引用。

支持的类型：docker_compose、volume、database、directory、file

示例：
  ops asset add blog-compose --type docker_compose --source /opt/blog --desc "Ghost blog"
  ops asset add blog-mysql --type database --source /var/lib/mysql --engine mysql --container blog-db`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id := args[0]

		if assetAddType == "" {
			return fmt.Errorf("--type is required (docker_compose, volume, database, directory, file)")
		}
		if assetAddSource == "" {
			return fmt.Errorf("--source is required")
		}

		a := asset.Asset{
			ID:          id,
			Type:        asset.Type(assetAddType),
			Source:      assetAddSource,
			Engine:      assetAddEngine,
			Container:   assetAddContainer,
			Description: assetAddDesc,
		}

		if assetAddExcludes != "" {
			for _, e := range strings.Split(assetAddExcludes, ",") {
				if trimmed := strings.TrimSpace(e); trimmed != "" {
					a.Excludes = append(a.Excludes, trimmed)
				}
			}
		}

		store := asset.NewDefaultStore()

		// Check if this is an update
		existing, _ := store.Get(id)
		if err := store.Save(a); err != nil {
			return fmt.Errorf("failed to save asset: %w", err)
		}

		if existing != nil {
			fmt.Printf("✅ Asset %q updated successfully in %s\n", id, store.FilePath())
		} else {
			fmt.Printf("✅ Asset %q (%s) saved successfully to %s\n", id, assetAddType, store.FilePath())
		}

		return nil
	},
}

var assetShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "查看指定资产的详细信息",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id := args[0]
		store := asset.NewDefaultStore()
		a, err := store.Get(id)
		if err != nil {
			return err
		}

		fmt.Printf("Asset: %s\n", a.ID)
		fmt.Printf("  Type        : %s\n", a.Type)
		fmt.Printf("  Source      : %s\n", a.Source)
		if a.Engine != "" {
			fmt.Printf("  Engine      : %s\n", a.Engine)
		}
		if a.Container != "" {
			fmt.Printf("  Container   : %s\n", a.Container)
		}
		if len(a.Excludes) > 0 {
			fmt.Printf("  Excludes    : %s\n", strings.Join(a.Excludes, ", "))
		}
		if a.Description != "" {
			fmt.Printf("  Description : %s\n", a.Description)
		}
		fmt.Printf("  Config      : %s\n", store.FilePath())

		return nil
	},
}

var assetRemoveCmd = &cobra.Command{
	Use:     "remove <id>",
	Aliases: []string{"rm", "delete"},
	Short:   "从配置中删除资产",
	Long: `从 assets.yaml 中删除一条资产记录。

若仍有备份作业在 assets: 里引用该资产，会打印一条警告；那个作业会在下次执行时失败，
但这不是保留一条失效资产记录的理由。删除从不阻塞，也从不追问。

示例：
  ops asset remove old-mysql`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		id := args[0]
		store := asset.NewDefaultStore()

		check := loadBackupRefCheck(backup.NewDefaultStore(), store)
		warnBackupRefCheck(os.Stderr, check, check.jobsReferencingAsset(id), fmt.Sprintf("asset %q", id))

		if err := store.Delete(id); err != nil {
			return err
		}

		fmt.Printf("✅ Asset %q removed successfully from %s\n", id, store.FilePath())
		return nil
	},
}

func completeAssetIDs(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	store := asset.NewDefaultStore()
	assets, err := store.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var comps []string
	for _, a := range assets {
		if a.Description != "" {
			comps = append(comps, fmt.Sprintf("%s\t%s (%s)", a.ID, a.Type, a.Description))
		} else {
			comps = append(comps, fmt.Sprintf("%s\t%s @ %s", a.ID, a.Type, a.Source))
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func completeAssetTypes(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{
		"docker_compose\tDocker Compose 项目目录",
		"volume\tDocker 命名数据卷或挂载数据目录",
		"database\t数据库逻辑导出 Dump（MySQL、PostgreSQL）",
		"directory\t通用配置或静态文件目录",
		"file\t单个关键文件或证书文件组",
	}, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	assetAddCmd.Flags().StringVar(&assetAddType, "type", "", "资产类型（docker_compose、volume、database、directory、file）")
	assetAddCmd.Flags().StringVar(&assetAddSource, "source", "", "服务器上的来源路径（必填）")
	assetAddCmd.Flags().StringVar(&assetAddEngine, "engine", "", "数据库引擎（mysql、postgres）——仅对 database 类型有效")
	assetAddCmd.Flags().StringVar(&assetAddContainer, "container", "", "Docker 容器名——仅对 database 类型有效")
	assetAddCmd.Flags().StringVar(&assetAddExcludes, "excludes", "", "逗号分隔的 glob 排除规则")
	assetAddCmd.Flags().StringVarP(&assetAddDesc, "desc", "d", "", "资产描述")

	_ = assetAddCmd.RegisterFlagCompletionFunc("type", completeAssetTypes)

	assetShowCmd.ValidArgsFunction = completeAssetIDs
	assetRemoveCmd.ValidArgsFunction = completeAssetIDs

	assetCmd.AddCommand(assetListCmd)
	assetCmd.AddCommand(assetAddCmd)
	assetCmd.AddCommand(assetShowCmd)
	assetCmd.AddCommand(assetRemoveCmd)

	rootCmd.AddCommand(assetCmd)
}
