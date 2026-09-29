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
	cleanName := filepath.Clean(name)
	if strings.Contains(cleanName, "..") || filepath.IsAbs(cleanName) {
		return nil, fmt.Errorf("invalid template name: %q", name)
	}

	// 1. Check custom directory first by filename
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

		// Also check if any custom template matches by Metadata.Name
		customs, _ := l.loadCustoms()
		for _, t := range customs {
			if t.Metadata.Name == name {
				tCopy := t
				return &tCopy, nil
			}
		}
	}

	// 2. Check built-in templates by filename
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

	// Also check if any built-in matches by Metadata.Name
	builtins, _ := l.loadBuiltins()
	for _, t := range builtins {
		if t.Metadata.Name == name {
			tCopy := t
			return &tCopy, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, name)
}

func (l *Loader) loadBuiltins() ([]Template, error) {
	return l.loadFromFS(builtin.FS, true, func(n string) string { return "builtin:" + n })
}

func (l *Loader) loadCustoms() ([]Template, error) {
	if l.customDir == "" {
		return nil, nil
	}

	if _, err := os.Stat(l.customDir); os.IsNotExist(err) {
		return nil, nil
	}

	return l.loadFromFS(os.DirFS(l.customDir), false, func(n string) string {
		return filepath.Join(l.customDir, n)
	})
}

// loadFromFS loads `.sh` templates from an `fs.FS`, skipping directories,
// unreadable files, and parse failures.
func (l *Loader) loadFromFS(fsys fs.FS, isBuiltin bool, sourcePath func(name string) string) ([]Template, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}

	var list []Template
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".sh")
		content, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			continue
		}
		tmpl, err := ParseTemplate(string(content), name)
		if err != nil {
			continue
		}
		tmpl.IsBuiltin = isBuiltin
		tmpl.SourcePath = sourcePath(entry.Name())
		list = append(list, *tmpl)
	}

	return list, nil
}
