package docker

import (
	"sort"
	"strings"
)

// sensitiveEnvNameMarkers are the name fragments that mark a container
// environment variable as likely carrying a credential. The check is
// deliberately name-based: classifying the value would mean reading secrets
// into the process, and a heuristic over values (entropy, length) would be both
// unreliable and impossible to explain to the user.
var sensitiveEnvNameMarkers = []string{
	"PASSWORD",
	"PASSWD",
	"PASSPHRASE",
	"SECRET",
	"TOKEN",
	"APIKEY",
	"API_KEY",
	"ACCESS_KEY",
	"ACCESSKEY",
	"PRIVATE_KEY",
	"CREDENTIAL",
}

// IsSensitiveEnvName reports whether an environment variable name suggests a
// credential (password, token, secret, API key, ...). Matching is
// case-insensitive and substring-based on purpose: real deployments spell these
// MYSQL_ROOT_PASSWORD, POSTGRES_PASSWORD, REDIS_PASSWORD, GITHUB_TOKEN,
// AWS_SECRET_ACCESS_KEY and so on.
func IsSensitiveEnvName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if upper == "" {
		return false
	}
	// "PWD" only counts inside a compound name (MYSQL_PWD carries a password).
	// A bare PWD is the working directory that shells and Docker inject into
	// nearly every container, and warning about it would train users to ignore
	// the report.
	segments := strings.FieldsFunc(upper, func(r rune) bool {
		return r == '_' || r == '-' || r == '.'
	})
	if len(segments) > 1 {
		for _, segment := range segments {
			if segment == "PWD" {
				return true
			}
		}
	}
	for _, marker := range sensitiveEnvNameMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// SensitiveEnvVars returns the sorted, de-duplicated names of the environment
// variables of a container whose name suggests a credential. Values are never
// returned: this feeds the risk report that has to be shown to the operator
// without turning the terminal into a secret store.
func SensitiveEnvVars(info *ContainerInfo) []string {
	if info == nil || len(info.Env) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(info.Env))
	var names []string
	for _, entry := range info.Env {
		name, _, _ := strings.Cut(entry, "=")
		name = strings.TrimSpace(name)
		if !IsSensitiveEnvName(name) {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}
