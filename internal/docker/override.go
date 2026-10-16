package docker

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeOverride is the target-side edit set apply writes into a restored
// Compose document so a service can be adapted to the destination host without
// renaming it: a replacement set of published ports and/or replacement host
// paths for bind mounts (keyed by the container-side mount destination).
type ComposeOverride struct {
	// Ports replaces the service's published ports when non-nil. A nil slice
	// keeps the ports the Compose file already declares.
	Ports []string
	// BindTargets maps a container mount destination (for example
	// "/etc/mysql/conf.d") to the host path the service should use on the
	// target. Only the listed entries are rewritten.
	BindTargets map[string]string
}

// Any reports whether the override changes anything.
func (o ComposeOverride) Any() bool {
	return o.Ports != nil || len(o.BindTargets) > 0
}

// OverrideCompose rewrites the published ports and the bind-mount host paths of
// one service inside a Compose document. Everything the override does not name
// is preserved verbatim (field by field, not byte for byte: comments and
// formatting are not retained). It never adds services, so a typo in the
// service key is reported instead of silently creating a new definition.
func OverrideCompose(doc []byte, serviceKey string, override ComposeOverride) ([]byte, error) {
	if !override.Any() {
		return doc, nil
	}
	key := strings.TrimSpace(serviceKey)
	if key == "" {
		return nil, errors.New("compose service key cannot be empty")
	}

	var root map[string]any
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("parse compose document: %w", err)
	}
	services, ok := asAnyMap(root["services"])
	if !ok {
		return nil, errors.New("compose document has no services mapping")
	}
	rawService, ok := services[key]
	if !ok {
		return nil, fmt.Errorf("compose document has no service named %q", key)
	}
	service, ok := asAnyMap(rawService)
	if !ok {
		return nil, fmt.Errorf("compose service %q is not a mapping", key)
	}

	if override.Ports != nil {
		ports := make([]any, 0, len(override.Ports))
		for _, p := range override.Ports {
			if p = strings.TrimSpace(p); p != "" {
				ports = append(ports, p)
			}
		}
		if len(ports) == 0 {
			delete(service, "ports")
		} else {
			service["ports"] = ports
		}
	}

	if len(override.BindTargets) > 0 {
		if volumes, ok := service["volumes"].([]any); ok {
			service["volumes"] = rewriteComposeVolumes(volumes, override.BindTargets)
		}
	}
	services[key] = service

	out, err := yaml.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshal compose document: %w", err)
	}
	return out, nil
}

// asAnyMap normalizes the mapping types yaml.v3 can produce for nested values
// (map[string]any with string keys, and the map[any]any older shapes emit).
func asAnyMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		out := make(map[string]any, len(typed))
		for k, v := range typed {
			key, ok := k.(string)
			if !ok {
				continue
			}
			out[key] = v
		}
		return out, true
	default:
		return nil, false
	}
}

// rewriteComposeVolumes replaces the host side of short-syntax volume entries
// whose container destination is named in bindTargets. Long-syntax (mapping)
// entries are left untouched: rewriting them silently would be guesswork.
func rewriteComposeVolumes(volumes []any, bindTargets map[string]string) []any {
	out := make([]any, 0, len(volumes))
	for _, entry := range volumes {
		spec, ok := entry.(string)
		if !ok {
			out = append(out, entry)
			continue
		}
		parts := strings.Split(spec, ":")
		if len(parts) >= 2 {
			if replacement, ok := bindTargets[parts[1]]; ok {
				parts[0] = replacement
				spec = strings.Join(parts, ":")
			}
		}
		out = append(out, spec)
	}
	return out
}
