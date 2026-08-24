package asset

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestAsset_Validate(t *testing.T) {
	tests := []struct {
		name    string
		asset   Asset
		wantErr error
	}{
		{
			name: "valid docker_compose asset",
			asset: Asset{
				ID:     "blog-compose",
				Type:   TypeDockerCompose,
				Source: "/opt/blog",
			},
			wantErr: nil,
		},
		{
			name: "valid database asset",
			asset: Asset{
				ID:        "blog-mysql",
				Type:      TypeDatabase,
				Source:    "/var/lib/mysql",
				Engine:    "mysql",
				Container: "blog-db",
			},
			wantErr: nil,
		},
		{
			name: "missing id",
			asset: Asset{
				Type:   TypeVolume,
				Source: "/data/volume",
			},
			wantErr: ErrInvalidAssetID,
		},
		{
			name: "invalid characters in id",
			asset: Asset{
				ID:     "blog compose with spaces!",
				Type:   TypeDirectory,
				Source: "/var/www",
			},
			wantErr: ErrInvalidAssetID,
		},
		{
			name: "unsupported type",
			asset: Asset{
				ID:     "my-asset",
				Type:   "unknown_type",
				Source: "/data",
			},
			wantErr: ErrInvalidAssetType,
		},
		{
			name: "missing source",
			asset: Asset{
				ID:   "my-asset",
				Type: TypeFile,
			},
			wantErr: ErrInvalidAssetSource,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.asset.Validate()
			if err != tt.wantErr {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestStore_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "assets.yaml")
	store := NewStore(filePath)

	if store.FilePath() != filePath {
		t.Errorf("expected FilePath() = %q, got %q", filePath, store.FilePath())
	}

	// 1. List on non-existent file
	assets, err := store.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(assets) != 0 {
		t.Errorf("expected 0 assets, got %d", len(assets))
	}

	// 2. Save asset
	a1 := Asset{
		ID:          "blog-compose",
		Type:        TypeDockerCompose,
		Source:      "/opt/blog",
		Description: "Ghost Blog Stack",
	}
	if err := store.Save(a1); err != nil {
		t.Fatalf("Save(a1) error: %v", err)
	}

	// 3. Get asset
	got, err := store.Get("blog-compose")
	if err != nil {
		t.Fatalf("Get('blog-compose') error: %v", err)
	}
	if got.ID != "blog-compose" || got.Source != "/opt/blog" {
		t.Errorf("unexpected asset: %+v", got)
	}

	// 4. Update asset
	a1.Description = "Updated description"
	if err := store.Save(a1); err != nil {
		t.Fatalf("Save(update) error: %v", err)
	}
	gotUpdated, _ := store.Get("blog-compose")
	if gotUpdated.Description != "Updated description" {
		t.Errorf("expected updated description, got %q", gotUpdated.Description)
	}

	// 5. Add second asset
	a2 := Asset{
		ID:     "blog-db",
		Type:   TypeDatabase,
		Source: "/var/lib/mysql",
		Engine: "mysql",
	}
	_ = store.Save(a2)

	// 6. GetMultiple
	mult, err := store.GetMultiple([]string{"blog-compose", "blog-db"})
	if err != nil {
		t.Fatalf("GetMultiple error: %v", err)
	}
	if len(mult) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(mult))
	}

	_, err = store.GetMultiple([]string{"blog-compose", "non-existent"})
	if err == nil {
		t.Error("expected error for missing asset in GetMultiple, got nil")
	}

	// 7. Delete asset
	if err := store.Delete("blog-compose"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	_, err = store.Get("blog-compose")
	if err == nil {
		t.Error("expected error for deleted asset, got nil")
	}
}

func TestStore_Concurrency(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(filepath.Join(tmpDir, "assets_concurrent.yaml"))

	var wg sync.WaitGroup
	workers := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			a := Asset{
				ID:     "asset-" + string(rune('a'+workerID)),
				Type:   TypeDirectory,
				Source: "/var/data",
			}
			if err := store.Save(a); err != nil {
				t.Errorf("concurrent Save error: %v", err)
			}
			if _, err := store.List(); err != nil {
				t.Errorf("concurrent List error: %v", err)
			}
		}(i)
	}

	wg.Wait()

	list, err := store.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(list) != workers {
		t.Errorf("expected %d assets, got %d", workers, len(list))
	}
}
