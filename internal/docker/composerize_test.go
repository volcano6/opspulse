package docker

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseInspectJSON_StandaloneContainer(t *testing.T) {
	rawJSON := `[
  {
    "Id": "1a2b3c4d5e6f",
    "Name": "/nginx-test",
    "Config": {
      "Image": "nginx:alpine",
      "Cmd": ["nginx", "-g", "daemon off;"],
      "Env": [
        "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "NGINX_PORT=80"
      ],
      "WorkingDir": "/app",
      "Labels": {
        "maintainer": "NGINX Docker Maintainers"
      }
    },
    "HostConfig": {
      "RestartPolicy": {
        "Name": "unless-stopped"
      },
      "PortBindings": {
        "80/tcp": [
          {
            "HostIp": "0.0.0.0",
            "HostPort": "8080"
          }
        ]
      },
      "Binds": [
        "/data/nginx/conf.d:/etc/nginx/conf.d:ro"
      ],
      "NetworkMode": "bridge"
    },
    "Mounts": [
      {
        "Type": "bind",
        "Source": "/data/nginx/html",
        "Destination": "/usr/share/nginx/html",
        "Mode": "rw",
        "RW": true
      }
    ]
  }
]`

	info, err := ParseInspectJSON([]byte(rawJSON))
	if err != nil {
		t.Fatalf("ParseInspectJSON() unexpected error: %v", err)
	}

	if info.Name != "nginx-test" {
		t.Errorf("info.Name = %q, want %q", info.Name, "nginx-test")
	}
	if info.Image != "nginx:alpine" {
		t.Errorf("info.Image = %q, want %q", info.Image, "nginx:alpine")
	}
	if info.RestartPolicy != "unless-stopped" {
		t.Errorf("info.RestartPolicy = %q, want %q", info.RestartPolicy, "unless-stopped")
	}
	if info.IsCompose() {
		t.Error("expected IsCompose() to be false for standalone container")
	}
	if info.IsDatabase() {
		t.Error("expected IsDatabase() to be false for nginx container")
	}
	if len(info.Ports) != 1 || info.Ports[0].String() != "8080:80" {
		t.Errorf("info.Ports = %+v, want 8080:80", info.Ports)
	}
	if len(info.Mounts) != 2 {
		t.Errorf("info.Mounts count = %d, want 2", len(info.Mounts))
	}
}

func TestParseInspectJSON_ComposeAndDatabase(t *testing.T) {
	rawJSON := `{
    "Id": "9f8e7d6c5b4a",
    "Name": "/blog-db-1",
    "Config": {
      "Image": "mysql:8.0",
      "Env": [
        "MYSQL_ROOT_PASSWORD=secret",
        "MYSQL_DATABASE=blog"
      ],
      "Labels": {
        "com.docker.compose.project": "blog",
        "com.docker.compose.service": "db",
        "com.docker.compose.project.working_dir": "/opt/blog"
      }
    },
    "HostConfig": {
      "RestartPolicy": {
        "Name": "always"
      },
      "PortBindings": {
        "3306/tcp": [
          {
            "HostIp": "127.0.0.1",
            "HostPort": "3306"
          }
        ]
      }
    },
    "Mounts": [
      {
        "Type": "volume",
        "Name": "mysql_data",
        "Source": "/var/lib/docker/volumes/mysql_data/_data",
        "Destination": "/var/lib/mysql",
        "RW": true
      }
    ]
}`

	info, err := ParseInspectJSON([]byte(rawJSON))
	if err != nil {
		t.Fatalf("ParseInspectJSON() unexpected error: %v", err)
	}

	if !info.IsCompose() {
		t.Error("expected IsCompose() to be true for Compose container")
	}
	if info.ComposeProject() != "blog" {
		t.Errorf("ComposeProject() = %q, want blog", info.ComposeProject())
	}
	if info.ComposeWorkingDir() != "/opt/blog" {
		t.Errorf("ComposeWorkingDir() = %q, want /opt/blog", info.ComposeWorkingDir())
	}
	if !info.IsDatabase() {
		t.Error("expected IsDatabase() to be true for mysql container")
	}
	if info.DatabaseEngine() != "mysql" {
		t.Errorf("DatabaseEngine() = %q, want mysql", info.DatabaseEngine())
	}
}

func TestParseInspectJSON_Errors(t *testing.T) {
	if _, err := ParseInspectJSON([]byte("")); err == nil {
		t.Error("expected error on empty input, got nil")
	}
	if _, err := ParseInspectJSON([]byte("not-json")); err == nil {
		t.Error("expected error on invalid JSON, got nil")
	}
	if _, err := ParseInspectJSON([]byte("[]")); err == nil {
		t.Error("expected error on empty array, got nil")
	}
}

func TestGenerateComposeYAML_RenamingAndNamedVolumes(t *testing.T) {
	info := &ContainerInfo{
		Name:          "nginx-test",
		Image:         "nginx:alpine",
		RestartPolicy: "unless-stopped",
		WorkingDir:    "/var/www",
		Env: []string{
			"PATH=/usr/local/bin",
			"CUSTOM_KEY=custom_val",
		},
		Ports: []PortMapping{
			{HostPort: "80", ContainerPort: "80", Protocol: "tcp"},
			{HostIP: "127.0.0.1", HostPort: "8443", ContainerPort: "443", Protocol: "tcp"},
		},
		Mounts: []VolumeMount{
			{Type: "bind", Source: "/host/nginx.conf", Destination: "/etc/nginx/nginx.conf", ReadOnly: true},
			{Type: "volume", Source: "nginx_cache", Destination: "/var/cache/nginx"},
		},
	}

	// Translate with alias "nginx"
	yamlOut, err := GenerateComposeYAML(info, "nginx")
	if err != nil {
		t.Fatalf("GenerateComposeYAML() error = %v", err)
	}

	// 1. Verify it unmarshals into valid YAML
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(yamlOut), &parsed); err != nil {
		t.Fatalf("generated YAML is invalid: %v\nYAML content:\n%s", err, yamlOut)
	}

	// 2. Check service name was renamed to "nginx"
	services, ok := parsed["services"].(map[string]any)
	if !ok || services["nginx"] == nil {
		t.Fatalf("services.nginx missing in YAML: %s", yamlOut)
	}

	svc := services["nginx"].(map[string]any)
	if svc["container_name"] != "nginx" {
		t.Errorf("container_name = %v, want nginx", svc["container_name"])
	}
	if svc["image"] != "nginx:alpine" {
		t.Errorf("image = %v, want nginx:alpine", svc["image"])
	}
	if svc["restart"] != "unless-stopped" {
		t.Errorf("restart = %v, want unless-stopped", svc["restart"])
	}

	// 3. Verify PATH was filtered out
	if strings.Contains(yamlOut, "PATH=/usr/local/bin") {
		t.Error("PATH environment variable should be filtered out")
	}
	if !strings.Contains(yamlOut, "CUSTOM_KEY=custom_val") {
		t.Error("CUSTOM_KEY should be preserved in environment")
	}

	// 4. Verify named volume declared at root level
	volumes, ok := parsed["volumes"].(map[string]any)
	if !ok {
		t.Fatalf("volumes section missing at root level: %s", yamlOut)
	}
	if _, exists := volumes["nginx_cache"]; !exists {
		t.Errorf("volumes.nginx_cache missing: %v", volumes)
	}
}

func TestDatabaseEngine_Postgres(t *testing.T) {
	info := &ContainerInfo{
		Image: "postgres:16-alpine",
	}
	if !info.IsDatabase() || info.DatabaseEngine() != "postgres" {
		t.Errorf("expected postgres, got engine=%q isDB=%v", info.DatabaseEngine(), info.IsDatabase())
	}
}
