package template

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/template/builtin"
)

// Loader handles discovery and loading of built-in and custom templates.
type Loader struct {
	customDir string
}

// NewLoader creates a Loader instance with a custom templates directory.
func NewLoader(customDir string) *Loader {
	return &Loader{
		customDir: customDir,
	}
}

// NewDefaultLoader creates a Loader pointing to $XDG_CONFIG_HOME/opspulse/templates.
func NewDefaultLoader() *Loader {
	dir := filepath.Join(config.Dir(), "templates")
	return NewLoader(dir)
}

// CustomDir returns the custom templates directory path.
func (l *Loader) CustomDir() string {
	return l.customDir
}

// List returns all available templates (built-in and custom, with custom overriding built-in).
func (l *Loader) List() ([]Template, error) {
	templateMap := make(map[string]Template)

	// 1. Load all built-in templates
	builtinList, err := l.loadBuiltins()
	if err != nil {
		return nil, fmt.Errorf("failed to load built-in templates: %w", err)
	}
	for _, t := range builtinList {
		templateMap[t.Metadata.Name] = t
	}

	// 2. Load custom templates (overriding built-in if same name)
	customList, err := l.loadCustoms()
	if err != nil {
		return nil, fmt.Errorf("failed to load custom templates: %w", err)
	}
	for _, t := range customList {
		templateMap[t.Metadata.Name] = t
	}

	// 3. Convert to sorted slice
	result := make([]Template, 0, len(templateMap))
	for _, t := range templateMap {
		result = append(result, t)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Metadata.Name < result[j].Metadata.Name
	})

	return result, nil
}

// Get returns a single template by name. Custom template takes precedence over built-in.
func (l *Loader) Get(name string) (*Template, error) {
	// 1. Check custom directory first
	if l.customDir != "" {
		customPath := filepath.Join(l.customDir, name+".sh")
		if _, err := os.Stat(customPath); err == nil {
			content, readErr := os.ReadFile(customPath)
			if readErr == nil {
				tmpl, parseErr := ParseTemplate(string(content), name)
				if parseErr == nil {
					tmpl.IsBuiltin = false
					tmpl.SourcePath = customPath
					return tmpl, nil
				}
			}
		}
	}

	// 2. Check built-in templates
	builtinPath := name + ".sh"
	content, err := builtin.FS.ReadFile(builtinPath)
	if err == nil {
		tmpl, parseErr := ParseTemplate(string(content), name)
		if parseErr == nil {
			tmpl.IsBuiltin = true
			tmpl.SourcePath = "builtin:" + builtinPath
			return tmpl, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
}

func (l *Loader) loadBuiltins() ([]Template, error) {
	entries, err := fs.ReadDir(builtin.FS, ".")
	if err != nil {
		return nil, err
	}

	var list []Template
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".sh")
		content, err := builtin.FS.ReadFile(entry.Name())
		if err != nil {
			continue
		}
		tmpl, err := ParseTemplate(string(content), name)
		if err != nil {
			continue
		}
		tmpl.IsBuiltin = true
		tmpl.SourcePath = "builtin:" + entry.Name()
		list = append(list, *tmpl)
	}

	return list, nil
}

func (l *Loader) loadCustoms() ([]Template, error) {
	if l.customDir == "" {
		return nil, nil
	}

	if _, err := os.Stat(l.customDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(l.customDir)
	if err != nil {
		return nil, err
	}

	var list []Template
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".sh")
		fullPath := filepath.Join(l.customDir, entry.Name())
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		tmpl, err := ParseTemplate(string(content), name)
		if err != nil {
			continue
		}
		tmpl.IsBuiltin = false
		tmpl.SourcePath = fullPath
		list = append(list, *tmpl)
	}

	return list, nil
}
