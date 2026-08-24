package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/volcano6/opspulse/internal/template"
)

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "Manage and inspect script templates",
	Long:  "List available built-in and custom script templates or inspect their contents.",
}

var templateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all available templates",
	RunE: func(_ *cobra.Command, _ []string) error {
		loader := template.NewDefaultLoader()
		list, err := loader.List()
		if err != nil {
			return fmt.Errorf("failed to list templates: %w", err)
		}

		if len(list) == 0 {
			fmt.Println("No templates found.")
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tVER\tTYPE\tOS\tDESCRIPTION")
		_, _ = fmt.Fprintln(tw, "----\t---\t----\t--\t-----------")

		for _, t := range list {
			tmplType := "built-in"
			if !t.IsBuiltin {
				tmplType = "custom"
			}

			osStr := "all"
			if len(t.Metadata.OS) > 0 {
				osStr = strings.Join(t.Metadata.OS, ",")
			}

			_, _ = fmt.Fprintf(tw, "%s\tv%d\t%s\t%s\t%s\n",
				t.Metadata.Name,
				t.Metadata.Version,
				tmplType,
				osStr,
				t.Metadata.Description,
			)
		}
		_ = tw.Flush()

		if loader.CustomDir() != "" {
			fmt.Printf("\nCustom templates directory: %s\n", loader.CustomDir())
		}
		return nil
	},
}

var templateShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show content and metadata of a script template",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		name := args[0]
		loader := template.NewDefaultLoader()
		tmpl, err := loader.Get(name)
		if err != nil {
			return err
		}

		typeStr := "built-in"
		if !tmpl.IsBuiltin {
			typeStr = fmt.Sprintf("custom (%s)", tmpl.SourcePath)
		}

		fmt.Printf("Template    : %s\n", tmpl.Metadata.Name)
		fmt.Printf("Version     : %d\n", tmpl.Metadata.Version)
		fmt.Printf("Type        : %s\n", typeStr)
		if len(tmpl.Metadata.OS) > 0 {
			fmt.Printf("OS Support  : %s\n", strings.Join(tmpl.Metadata.OS, ", "))
		}
		if tmpl.Metadata.Description != "" {
			fmt.Printf("Description : %s\n", tmpl.Metadata.Description)
		}
		fmt.Println("------------------------------------------------------------")
		fmt.Print(tmpl.Content)
		if !strings.HasSuffix(tmpl.Content, "\n") {
			fmt.Println()
		}
		return nil
	},
}

func completeTemplateNames(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	loader := template.NewDefaultLoader()
	templates, err := loader.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	var comps []string
	for _, t := range templates {
		if t.Metadata.Description != "" {
			comps = append(comps, fmt.Sprintf("%s\t%s", t.Metadata.Name, t.Metadata.Description))
		} else {
			comps = append(comps, t.Metadata.Name)
		}
	}
	return comps, cobra.ShellCompDirectiveNoFileComp
}

func init() {
	templateShowCmd.ValidArgsFunction = completeTemplateNames

	templateCmd.AddCommand(templateListCmd)
	templateCmd.AddCommand(templateShowCmd)

	rootCmd.AddCommand(templateCmd)
}
