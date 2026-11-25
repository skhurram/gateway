package config_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gateway/internal/config"
)

func TestWatchDetectsAtomicFileReplacement(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	changed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- config.Watch(ctx, target, func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("watch: %v", err)
		}
	})

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		replacement := filepath.Join(dir, "replacement")
		if err := os.WriteFile(replacement, []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, target); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changed:
			return
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("directory replacement was not observed")
		}
	}
}
