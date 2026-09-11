package redash

import (
	"sync"
	"time"
)

// limiter is a token bucket that refuses rather than waits. A tool call that
// blocks for most of a minute looks like a hang to the person watching the
// assistant, so the caller gets an error saying when to retry instead.
type limiter struct {
	mu       sync.Mutex
	capacity float64
	tokens   float64
	last     time.Time
	now      func() time.Time
}

func newLimiter(perMinute int, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	c := float64(max(perMinute, 1))
	return &limiter{capacity: c, tokens: c, last: now(), now: now}
}

// take consumes a token, or reports how long until one is available.
func (l *limiter) take() (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	t := l.now()
	l.tokens = min(l.capacity, l.tokens+t.Sub(l.last).Seconds()*l.capacity/60)
	l.last = t

	if l.tokens >= 1 {
		l.tokens--
		return true, 0
	}
	return false, time.Duration((1 - l.tokens) * 60 / l.capacity * float64(time.Second))
}
