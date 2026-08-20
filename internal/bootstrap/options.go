// Package bootstrap orchestrates server provisioning workflows using templates and executors.
package bootstrap

// RunOptions defines options for a bootstrap execution run.
type RunOptions struct {
	ServerNames   []string
	TemplateNames []string
	DryRun        bool
	StopOnError   bool
}
