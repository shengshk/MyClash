package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseBackupCron(t *testing.T) {
	min, hour, days, err := parseBackupCron("0 2 */7 * *")
	if err != nil || min != 0 || hour != 2 || days != 7 {
		t.Fatalf("got %d %d %d err=%v", min, hour, days, err)
	}
	min, hour, days, err = parseBackupCron("")
	if err != nil || min != 0 || hour != 2 || days != 7 {
		t.Fatalf("default got %d %d %d err=%v", min, hour, days, err)
	}
}

func TestBackupCreatePrune(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "aio.yaml")
	if err := os.WriteFile(yamlPath, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bs := newBackupStore(yamlPath, 2)
	if _, err := bs.Create(backupKindManual); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // stamp second resolution
	if err := os.WriteFile(yamlPath, []byte("a: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Create(backupKindManual); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := os.WriteFile(yamlPath, []byte("a: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Create(backupKindAuto); err != nil {
		t.Fatal(err)
	}
	list, err := bs.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 backups after prune, got %d", len(list))
	}
}
