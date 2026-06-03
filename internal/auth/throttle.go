package auth

import (
	"sync"
	"time"
)

// LoginThrottle is an in-memory sliding-window counter of failed login
// attempts keyed by (client_ip, username). Keying on the tuple means an
// attacker spamming a victim's username from one IP gets throttled without
// locking the legit user out from another IP.
//
// The window is approximated by storing attempt timestamps and pruning on
// every read; for a homelab tool with a handful of users that's cheap.
type LoginThrottle struct {
	window   time.Duration
	maxFails int
	cooldown time.Duration
	mu       sync.Mutex
	attempts map[string][]time.Time
}

// NewLoginThrottle creates a throttle that allows maxFails attempts within
// window before locking out a key for cooldown.
func NewLoginThrottle(window time.Duration, maxFails int, cooldown time.Duration) *LoginThrottle {
	return &LoginThrottle{
		window:   window,
		maxFails: maxFails,
		cooldown: cooldown,
		attempts: make(map[string][]time.Time),
	}
}

// DefaultLoginThrottle returns the production-tuned throttle: 5 failures per
// 15 minutes, then 60-second cooldown.
func DefaultLoginThrottle() *LoginThrottle {
	return NewLoginThrottle(15*time.Minute, 5, time.Minute)
}

// Allow reports whether a new attempt for the key is permitted right now.
// When blocked, the second return value is the time the caller should
// Retry-After.
func (t *LoginThrottle) Allow(key string) (bool, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-t.window)
	t.attempts[key] = prune(t.attempts[key], cutoff)
	if len(t.attempts[key]) < t.maxFails {
		return true, time.Time{}
	}
	last := t.attempts[key][len(t.attempts[key])-1]
	retry := last.Add(t.cooldown)
	if now.After(retry) {
		// Cooldown elapsed since last failure — let one through. If it
		// fails too, we'll add another timestamp and keep them locked.
		return true, time.Time{}
	}
	return false, retry
}

// RecordFailure adds a failure timestamp for the key.
func (t *LoginThrottle) RecordFailure(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts[key] = append(t.attempts[key], time.Now())
}

// Reset clears the counter for the key (call on successful login).
func (t *LoginThrottle) Reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, key)
}

func prune(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for ; i < len(ts); i++ {
		if ts[i].After(cutoff) {
			break
		}
	}
	if i == 0 {
		return ts
	}
	return append([]time.Time(nil), ts[i:]...)
}
