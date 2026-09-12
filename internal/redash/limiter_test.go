package redash

import (
	"testing"
	"time"
)

func TestLimiterRefusesInsteadOfWaiting(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(2, func() time.Time { return now })

	for i := 0; i < 2; i++ {
		if ok, _ := l.take(); !ok {
			t.Fatalf("take %d was refused inside the budget", i)
		}
	}
	ok, wait := l.take()
	if ok {
		t.Fatal("a third take in the same instant should be refused")
	}
	if wait <= 0 || wait > 30*time.Second {
		t.Fatalf("wait = %s, want at most 30s at 2 per minute", wait)
	}

	now = now.Add(30 * time.Second)
	if ok, _ := l.take(); !ok {
		t.Fatal("a token should have refilled after 30s")
	}
}

func TestLimiterNeverExceedsCapacity(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(3, func() time.Time { return now })

	now = now.Add(time.Hour)
	granted := 0
	for i := 0; i < 10; i++ {
		if ok, _ := l.take(); ok {
			granted++
		}
	}
	if granted != 3 {
		t.Fatalf("an idle hour must not bank more than one minute of budget, granted %d", granted)
	}
}
