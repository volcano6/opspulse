package backup

import (
	"path"
	"sort"
	"strings"
)

// PathRemapper handles path translations for cross-machine restores.
type PathRemapper struct {
	Rules map[string]string
}

// NewPathRemapper initializes a new PathRemapper from a rule map.
func NewPathRemapper(rules map[string]string) *PathRemapper {
	normalized := make(map[string]string)
	for k, v := range rules {
		cleanKey := path.Clean(k)
		if !strings.HasPrefix(cleanKey, "/") {
			cleanKey = "/" + cleanKey
		}
		
		cleanVal := path.Clean(v)
		if !strings.HasPrefix(cleanVal, "/") {
			cleanVal = "/" + cleanVal
		}
		
		normalized[cleanKey] = cleanVal
	}
	return &PathRemapper{Rules: normalized}
}

// Remap takes an original absolute path and applies the longest matching
// source prefix to map it to the new target prefix.
func (r *PathRemapper) Remap(origPath string) string {
	if len(r.Rules) == 0 {
		return origPath
	}

	cleanOrig := path.Clean(origPath)
	if !strings.HasPrefix(cleanOrig, "/") {
		cleanOrig = "/" + cleanOrig
	}

	// Sort keys by length descending to match longest prefix first
	var prefixes []string
	for k := range r.Rules {
		prefixes = append(prefixes, k)
	}
	sort.Slice(prefixes, func(i, j int) bool {
		return len(prefixes[i]) > len(prefixes[j])
	})

	for _, prefix := range prefixes {
		// Exact match or prefix match with trailing slash
		if cleanOrig == prefix || strings.HasPrefix(cleanOrig, prefix+"/") {
			targetPrefix := r.Rules[prefix]
			trimmed := strings.TrimPrefix(cleanOrig, prefix)
			return path.Join(targetPrefix, trimmed)
		}
	}

	return origPath
}
