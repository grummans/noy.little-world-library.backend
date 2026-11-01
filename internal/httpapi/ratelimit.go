package httpapi

import (
	"sync"
	"time"
)

// limiter is a fixed-window counter used to slow down credential stuffing and to
// keep bcrypt from being used as a CPU amplifier on the public auth endpoints.
type limiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	entries map[string]windowEntry
}

type windowEntry struct {
	count int
	reset time.Time
}

func newLimiter(window time.Duration, max int) *limiter {
	return &limiter{window: window, max: max, entries: map[string]windowEntry{}}
}

// allow records a hit for key and reports whether it is still within the budget.
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if len(l.entries) > 1024 {
		l.sweep(now)
	}

	entry, ok := l.entries[key]
	if !ok || now.After(entry.reset) {
		l.entries[key] = windowEntry{count: 1, reset: now.Add(l.window)}
		return true
	}
	if entry.count >= l.max {
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}

func (l *limiter) sweep(now time.Time) {
	for key, entry := range l.entries {
		if now.After(entry.reset) {
			delete(l.entries, key)
		}
	}
}
