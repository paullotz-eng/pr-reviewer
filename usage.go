package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// usageWindow is one Claude plan limit window, as reported by the claude CLI.
type usageWindow struct {
	Utilization float64 `json:"utilization"` // 0..1 of the window's quota used
	ResetsAt    int64   `json:"resetsAt"`    // unix seconds
	Status      string  `json:"status"`
}

// Usage is the latest plan usage seen, keyed by window ("five_hour", "seven_day").
type Usage struct {
	Windows map[string]usageWindow `json:"windows"`
	Updated time.Time              `json:"updated"`
}

// usageTracker keeps the latest plan usage that claude reported, persisted so
// it is known again after a restart.
type usageTracker struct {
	mu   sync.Mutex
	path string
	u    Usage
}

func loadUsage(path string) *usageTracker {
	t := &usageTracker{path: path}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &t.u)
	}
	return t
}

func (t *usageTracker) get() Usage {
	if t == nil {
		return Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.u
}

// record reads the rate_limit_event lines of claude's stream-json output.
func (t *usageTracker) record(lines [][]byte) {
	if t == nil {
		return
	}
	var latest map[string]usageWindow
	for _, l := range lines {
		var ev struct {
			Type string `json:"type"`
			Info struct {
				Windows map[string]usageWindow `json:"unifiedWindows"`
			} `json:"rate_limit_info"`
		}
		if json.Unmarshal(l, &ev) == nil && ev.Type == "rate_limit_event" && len(ev.Info.Windows) > 0 {
			latest = ev.Info.Windows
		}
	}
	if latest == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.u = Usage{Windows: latest, Updated: time.Now()}
	if b, err := json.Marshal(t.u); err == nil {
		os.MkdirAll(filepath.Dir(t.path), 0o755)
		os.WriteFile(t.path, b, 0o644)
	}
}
