package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigFileLookupPrefersLocalThenUserConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG_CONFIG_HOME is Unix-specific")
	}

	root := t.TempDir()
	userRoot := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", userRoot)
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("CONFIG_FILE_BAG", "")
	oldOut, hadOut := os.LookupEnv("BAG_OUT_DIR")
	if err := os.Unsetenv("BAG_OUT_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOut {
			_ = os.Setenv("BAG_OUT_DIR", oldOut)
		} else {
			_ = os.Unsetenv("BAG_OUT_DIR")
		}
	})

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	userFile := filepath.Join(userRoot, ServiceName, "bag.yaml")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte("out_dir: user-out\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	localFile := filepath.Join(root, "bag.yaml")
	if err := os.WriteFile(localFile, []byte("out_dir: local-out\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutDir != "local-out" {
		t.Fatalf("local OutDir = %q, want local-out", cfg.OutDir)
	}

	if err := os.Remove(localFile); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OutDir != "user-out" {
		t.Fatalf("user OutDir = %q, want user-out", cfg.OutDir)
	}
}

func TestConfigFolders(t *testing.T) {
	dirs := configFolders()
	if len(dirs) < 2 {
		t.Fatalf("config folders = %v", dirs)
	}

	wantSystem := filepath.Join(string(filepath.Separator), "etc", ServiceName)
	if dirs[len(dirs)-2] != wantSystem || dirs[len(dirs)-1] != filepath.Join(string(filepath.Separator), "etc") {
		t.Fatalf("system config folders = %v", dirs)
	}
}
