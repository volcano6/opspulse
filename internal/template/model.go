// Package template manages built-in and user-defined script templates for server automation.
package template

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	// ErrTemplateNotFound is returned when a requested template does not exist.
	ErrTemplateNotFound = errors.New("template not found")
	// ErrInvalidMetadata is returned when template metadata cannot be parsed.
	ErrInvalidMetadata = errors.New("invalid template metadata")
)

// Metadata defines the metadata header of a script template.
type Metadata struct {
	Name        string   `yaml:"name"`
	Version     int      `yaml:"version"`
	OS          []string `yaml:"os,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

// Template represents an executable script template with metadata.
type Template struct {
	Metadata   Metadata
	Content    string
	IsBuiltin  bool
	SourcePath string
}

// ParseTemplate parses a raw shell script and extracts YAML metadata if present in the header.
func ParseTemplate(raw string, defaultName string) (*Template, error) {
	meta, err := ExtractMetadata(raw)
	if err != nil {
		// Fallback to minimal metadata if none is provided
		meta = &Metadata{
			Name:        defaultName,
			Version:     1,
			Description: "Custom script template",
		}
	} else if meta.Name == "" {
		meta.Name = defaultName
	}

	return &Template{
		Metadata: *meta,
		Content:  raw,
	}, nil
}

// ExtractMetadata extracts YAML metadata block enclosed between `# ---` in the script header.
func ExtractMetadata(script string) (*Metadata, error) {
	scanner := bufio.NewScanner(strings.NewReader(script))
	var yamlLines []string
	inFrontmatter := false
	found := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "# ---" || line == "---" {
			if !inFrontmatter {
				inFrontmatter = true
				continue
			}
			found = true
			break
		}

		if inFrontmatter {
			// Strip leading '#' if commented
			cleaned := strings.TrimPrefix(line, "#")
			cleaned = strings.TrimPrefix(cleaned, " ")
			yamlLines = append(yamlLines, cleaned)
		}
	}

	if !found || len(yamlLines) == 0 {
		return nil, ErrInvalidMetadata
	}

	yamlData := strings.Join(yamlLines, "\n")
	var meta Metadata
	if err := yaml.Unmarshal([]byte(yamlData), &meta); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
	}

	return &meta, nil
}
