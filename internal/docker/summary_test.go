package docker

import "testing"

func TestParsePsJSON(t *testing.T) {
	raw := `{"ID":"1a2b3c4d5e6f","Names":"blog-web","Image":"nginx:alpine","Command":"nginx","State":"running","Status":"Up 2 days","Ports":"0.0.0.0:80->80/tcp","Labels":"com.docker.compose.project=blog,com.docker.compose.service=web"}
{"ID":"9z8y7x6w5v4u","Names":"wild-db","Image":"mysql:8.0","Command":"mysqld","State":"running","Status":"Up 1 hour","Ports":"3306/tcp","Labels":""}
`

	items, err := ParsePsJSON(raw)
	if err != nil {
		t.Fatalf("ParsePsJSON() error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(items))
	}

	compose := items[0]
	if compose.Name != "blog-web" || !compose.IsCompose() {
		t.Fatalf("first container = %+v, want a Compose-managed blog-web", compose)
	}
	if compose.ComposeProject() != "blog" || compose.ComposeService() != "web" {
		t.Errorf("compose project/service = %q/%q, want blog/web", compose.ComposeProject(), compose.ComposeService())
	}

	plain := items[1]
	if plain.IsCompose() {
		t.Errorf("second container should not be Compose-managed: %+v", plain)
	}
	if got := plain.DatabaseEngine(); got != "mysql" {
		t.Errorf("DatabaseEngine() = %q, want mysql", got)
	}
}

func TestParsePsJSONSkipsDockerMissingSentinel(t *testing.T) {
	items, err := ParsePsJSON("ERR_DOCKER_NOT_FOUND\n")
	if err != nil {
		t.Fatalf("ParsePsJSON() error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no containers, got %d", len(items))
	}
}

func TestParsePsJSONRejectsMalformedLine(t *testing.T) {
	if _, err := ParsePsJSON("not-json\n"); err == nil {
		t.Fatal("ParsePsJSON() should reject a malformed line")
	}
}

func TestParseLabels(t *testing.T) {
	labels := ParseLabels("a=1, b=2 ,c=,empty,=no-key")
	if labels["a"] != "1" || labels["b"] != "2" {
		t.Errorf("labels = %v, want a=1 b=2", labels)
	}
	if v, ok := labels["c"]; !ok || v != "" {
		t.Errorf("labels[c] = %q (present=%v), want an empty value", v, ok)
	}
	if _, ok := labels["=no-key"]; ok {
		t.Error("a label without a key should be dropped")
	}
	if len(ParseLabels("")) != 0 {
		t.Error("empty input should yield no labels")
	}
}
