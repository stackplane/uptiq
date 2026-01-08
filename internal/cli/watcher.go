package cli

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// ConfigWatcher watches a config file for changes.
type ConfigWatcher struct {
	watcher       *fsnotify.Watcher
	log           *slog.Logger
	targetFile    string
	triggerReload func()
}

// Watcher configuration constants.
const (
	watcherDebounce = 250 * time.Millisecond
)

// NewConfigWatcher creates a watcher for the given config file.
func NewConfigWatcher(log *slog.Logger, configPath string, triggerReload func()) (*ConfigWatcher, error) {
	if log == nil {
		log = slog.Default()
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}

	abs, err := filepath.Abs(configPath)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("abs path: %w", err)
	}

	dir := filepath.Dir(abs)
	base := filepath.Base(abs)

	// Watch the directory so atomic-save (rename) patterns still trigger
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch dir %q: %w", dir, err)
	}

	cw := &ConfigWatcher{
		watcher:       w,
		log:           log,
		targetFile:    base,
		triggerReload: triggerReload,
	}

	go cw.run()

	return cw, nil
}

// Close stops the watcher.
func (cw *ConfigWatcher) Close() error {
	return cw.watcher.Close()
}

func (cw *ConfigWatcher) run() {
	var (
		timer   *time.Timer
		pending bool
	)

	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	resetTimer := func() {
		if timer == nil {
			timer = time.NewTimer(watcherDebounce)
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(watcherDebounce)
	}

	for {
		var timerCh <-chan time.Time
		if timer != nil {
			timerCh = timer.C
		}

		select {
		case ev, ok := <-cw.watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != cw.targetFile {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				pending = true
				resetTimer()
			}

		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return
			}
			cw.log.Warn("fsnotify error", "error", err.Error())

		case <-timerCh:
			if pending {
				pending = false
				cw.triggerReload()
			}
		}
	}
}
