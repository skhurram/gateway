package config

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const watchDebounce = 50 * time.Millisecond

// Watch calls changed after the parent directory of path changes. Watching the
// directory, rather than the file, survives Kubernetes ConfigMap symlink
// swaps.
func Watch(ctx context.Context, path string, changed func()) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create config watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	dir := filepath.Clean(filepath.Dir(path))
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("watch config directory %q: %w", dir, err)
	}

	timer := time.NewTimer(watchDebounce)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			return fmt.Errorf("watch config directory %q: %w", dir, err)
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op != 0 {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(watchDebounce)
			}
		case <-timer.C:
			changed()
		}
	}
}
