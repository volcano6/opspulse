package backup

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestJob_Validate(t *testing.T) {
	tests := []struct {
		name    string
		job     Job
		wantErr error
	}{
		{
			name: "valid job",
			job: Job{
				Name:    "vps-01-data",
				Server:  "vps-01",
				Paths:   []string{"/etc/nginx", "/var/www"},
				Backend: "s3:s3.amazonaws.com/my-bucket",
			},
			wantErr: nil,
		},
		{
			name: "missing name",
			job: Job{
				Server:  "vps-01",
				Paths:   []string{"/data"},
				Backend: "/mnt/backup",
			},
			wantErr: ErrInvalidJobName,
		},
		{
			name: "missing server",
			job: Job{
				Name:    "vps-data",
				Paths:   []string{"/data"},
				Backend: "/mnt/backup",
			},
			wantErr: ErrInvalidJobServer,
		},
		{
			name: "missing paths",
			job: Job{
				Name:    "vps-data",
				Server:  "vps-01",
				Backend: "/mnt/backup",
			},
			wantErr: ErrInvalidJobPaths,
		},
		{
			name: "empty path string",
			job: Job{
				Name:    "vps-data",
				Server:  "vps-01",
				Paths:   []string{"   "},
				Backend: "/mnt/backup",
			},
			wantErr: ErrInvalidJobPaths,
		},
		{
			name: "missing backend",
			job: Job{
				Name:   "vps-data",
				Server: "vps-01",
				Paths:  []string{"/data"},
			},
			wantErr: ErrInvalidJobBackend,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.job.Validate()
			if err != tt.wantErr {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestStore_Lifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "backups.yaml")
	store := NewStore(filePath)

	if store.FilePath() != filePath {
		t.Errorf("expected FilePath() = %q, got %q", filePath, store.FilePath())
	}

	// 1. List on non-existent file
	jobs, err := store.List()
	if err != nil {
		t.Fatalf("List() on empty store error: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("expected 0 jobs initially, got %d", len(jobs))
	}

	// 2. Save new job
	job1 := Job{
		Name:    "web-backup",
		Server:  "vps-01",
		Paths:   []string{"/var/www", "/etc/nginx"},
		Backend: "s3:s3.amazonaws.com/backup-bucket",
		Retention: &RetentionPolicy{
			KeepDaily:   7,
			KeepWeekly:  4,
			KeepMonthly: 6,
		},
		Tags:        []string{"prod", "web"},
		Description: "Production web server backup",
	}

	if err := store.Save(job1); err != nil {
		t.Fatalf("Save(job1) error: %v", err)
	}

	// 3. Get job
	got, err := store.Get("web-backup")
	if err != nil {
		t.Fatalf("Get('web-backup') error: %v", err)
	}
	if got.Name != "web-backup" || got.Server != "vps-01" {
		t.Errorf("unexpected job: %+v", got)
	}
	if got.Retention == nil || got.Retention.KeepDaily != 7 {
		t.Errorf("unexpected retention: %+v", got.Retention)
	}

	// 4. Update existing job
	job1.Description = "Updated description"
	job1.Paths = append(job1.Paths, "/var/log")
	if err := store.Save(job1); err != nil {
		t.Fatalf("Save(update) error: %v", err)
	}

	gotUpdated, err := store.Get("web-backup")
	if err != nil {
		t.Fatalf("Get after update error: %v", err)
	}
	if gotUpdated.Description != "Updated description" || len(gotUpdated.Paths) != 3 {
		t.Errorf("expected updated fields, got %+v", gotUpdated)
	}

	// 5. Add second job
	job2 := Job{
		Name:    "db-backup",
		Server:  "vps-02",
		Paths:   []string{"/var/lib/postgresql"},
		Backend: "/mnt/backup/db",
	}
	if err := store.Save(job2); err != nil {
		t.Fatalf("Save(job2) error: %v", err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(list))
	}

	// 6. Delete job
	if err := store.Delete("web-backup"); err != nil {
		t.Fatalf("Delete('web-backup') error: %v", err)
	}

	// 7. Get deleted job should return error
	_, err = store.Get("web-backup")
	if err == nil {
		t.Error("expected error for deleted job, got nil")
	}

	// 8. Delete non-existent job
	if err := store.Delete("non-existent"); err == nil {
		t.Error("expected error for non-existent job delete, got nil")
	}
}

func TestStore_Concurrency(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(filepath.Join(tmpDir, "backups_concurrent.yaml"))

	var wg sync.WaitGroup
	workers := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			job := Job{
				Name:    "job-" + string(rune('a'+workerID)),
				Server:  "vps-01",
				Paths:   []string{"/data"},
				Backend: "/backup",
			}
			if err := store.Save(job); err != nil {
				t.Errorf("concurrent Save error: %v", err)
			}
			if _, err := store.List(); err != nil {
				t.Errorf("concurrent List error: %v", err)
			}
		}(i)
	}

	wg.Wait()

	jobs, err := store.List()
	if err != nil {
		t.Fatalf("List() after concurrency error: %v", err)
	}
	if len(jobs) != workers {
		t.Errorf("expected %d jobs, got %d", workers, len(jobs))
	}
}

func TestStoreReadRejectsInvalidConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backups.yaml")
	data := []byte("backups:\n  - name: duplicate\n    server: local\n    paths: [/one]\n    backend: /repo\n  - name: duplicate\n    server: local\n    paths: [/two]\n    backend: /repo\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).List(); err == nil {
		t.Fatal("List() accepted duplicate backup names")
	}
}
