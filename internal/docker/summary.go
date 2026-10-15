package docker

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ContainerSummary is the lightweight view of a container as reported by
// `docker ps --format '{{json .}}'`. It carries just enough metadata to decide
// which containers are Compose-managed and therefore worth a deeper
// `docker inspect`, without paying for one inspect round-trip per container.
type ContainerSummary struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Command string            `json:"command,omitempty"`
	State   string            `json:"state,omitempty"`
	Status  string            `json:"status,omitempty"`
	Ports   string            `json:"ports,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// IsCompose reports whether the container carries Docker Compose project labels.
func (c ContainerSummary) IsCompose() bool {
	return c.Labels[LabelComposeProject] != ""
}

// ComposeProject returns the Compose project name, or "" for a plain container.
func (c ContainerSummary) ComposeProject() string {
	return c.Labels[LabelComposeProject]
}

// ComposeService returns the Compose service name, or "" for a plain container.
func (c ContainerSummary) ComposeService() string {
	return c.Labels[LabelComposeService]
}

// DatabaseEngine identifies the database dialect ("mysql" or "postgres") from
// the container image name, or "" when the image is not a recognized database.
func (c ContainerSummary) DatabaseEngine() string {
	return databaseImageEngines[imageRepoName(c.Image)]
}

// rawPSLine mirrors the JSON object emitted by `docker ps --format '{{json .}}'`.
// Docker renders the Labels field of that view as a comma-separated "k=v" list.
type rawPSLine struct {
	ID      string `json:"ID"`
	Names   string `json:"Names"`
	Image   string `json:"Image"`
	Command string `json:"Command"`
	State   string `json:"State"`
	Status  string `json:"Status"`
	Ports   string `json:"Ports"`
	Labels  string `json:"Labels"`
}

// ParsePsJSON parses newline-delimited `docker ps --format '{{json .}}'` output
// into structured summaries. Blank lines are skipped, and the
// "ERR_DOCKER_NOT_FOUND" sentinel emitted by the discovery script when Docker
// is absent is ignored so callers see an empty list rather than a parse error.
func ParsePsJSON(raw string) ([]ContainerSummary, error) {
	var out []ContainerSummary
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "ERR_DOCKER_NOT_FOUND" {
			continue
		}

		var item rawPSLine
		if err := json.Unmarshal([]byte(trimmed), &item); err != nil {
			return nil, fmt.Errorf("invalid docker ps json %q: %w", trimmed, err)
		}

		out = append(out, ContainerSummary{
			ID:      item.ID,
			Name:    strings.TrimPrefix(item.Names, "/"),
			Image:   item.Image,
			Command: item.Command,
			State:   item.State,
			Status:  item.Status,
			Ports:   item.Ports,
			Labels:  ParseLabels(item.Labels),
		})
	}
	return out, nil
}

// ParseLabels splits Docker's comma-separated "k=v,k2=v2" label string into a
// map. Empty input yields an empty (non-nil) map so callers can index it
// without a nil check.
func ParseLabels(raw string) map[string]string {
	labels := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		labels[key] = strings.TrimSpace(value)
	}
	return labels
}
