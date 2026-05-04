// Package watcher watches the filesystem for changes and notifies listeners.
package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Event describes a filesystem change.
type Event struct {
	Path string
	Op   string // "write" | "remove" | "create"
}

// Watcher polls a directory tree for changes.
type Watcher struct {
	root     string
	interval time.Duration
	onChange func(Event)

	mu     sync.Mutex
	seen   map[string]time.Time
	stopCh chan struct{}
}

// New creates a Watcher rooted at dir, calling onChange on every detected change.
func New(dir string, interval time.Duration, onChange func(Event)) *Watcher {
	return &Watcher{
		root:     dir,
		interval: interval,
		onChange: onChange,
		seen:     make(map[string]time.Time),
		stopCh:   make(chan struct{}),
	}
}

// Start begins polling in a background goroutine.
func (w *Watcher) Start() {
	_, _ = w.snapshot()
	go w.loop()
}

// Stop shuts down the polling loop.
func (w *Watcher) Stop() {
	close(w.stopCh)
}

func (w *Watcher) loop() {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.check()
		case <-w.stopCh:
			return
		}
	}
}

func (w *Watcher) check() {
	current, err := w.snapshot()
	if err != nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	for path, modTime := range current {
		prev, existed := w.seen[path]
		if !existed {
			w.onChange(Event{Path: path, Op: "create"})
		} else if modTime.After(prev) {
			w.onChange(Event{Path: path, Op: "write"})
		}
	}

	for path := range w.seen {
		if _, ok := current[path]; !ok {
			w.onChange(Event{Path: path, Op: "remove"})
		}
	}

	w.seen = current
}

func (w *Watcher) snapshot() (map[string]time.Time, error) {
	result := make(map[string]time.Time)
	err := filepath.WalkDir(w.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		base := d.Name()

		// Skip directories that don't contain user-edited source
		if d.IsDir() {
			switch base {
			case ".git", "vendor", "dist", "node_modules", ".gowave-cache":
				return filepath.SkipDir
			}
			return nil
		}

		// Skip gowave-generated files — they are written and deleted on every
		// build and would cause an infinite rebuild loop in the dev server.
		if strings.HasPrefix(base, "_gowave_") {
			return nil
		}

		info, err := d.Info()
		if err == nil {
			result[path] = info.ModTime()
		}
		return nil
	})

	w.mu.Lock()
	if len(w.seen) == 0 {
		w.seen = result
	}
	w.mu.Unlock()

	return result, err
}
