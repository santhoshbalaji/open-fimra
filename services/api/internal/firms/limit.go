package firms

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// TransactionLimit and TransactionWindow are NASA's published limit for
	// one map key: 5,000 transactions in each 10-minute interval.
	TransactionLimit  = 5000
	TransactionWindow = 10 * time.Minute
)

// Limiter keeps the transactions sent in any sliding window at or under a
// limit. It only counts this process's requests. Other programs that use the
// same key need their own share of the limit.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error

	mu    sync.Mutex
	spent []spend
	total int
}

type spend struct {
	at   time.Time
	cost int
}

func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, now: time.Now, sleep: sleepContext}
}

// WithClock replaces the clock and sleep function, for tests.
func (l *Limiter) WithClock(now func() time.Time, sleep func(context.Context, time.Duration) error) *Limiter {
	l.now, l.sleep = now, sleep
	return l
}

// Wait blocks until cost more transactions fit in the window, then records
// them. It returns early with the context's error.
func (l *Limiter) Wait(ctx context.Context, cost int) error {
	if cost < 1 || cost > l.limit {
		return fmt.Errorf("a request of %d transactions cannot fit a limit of %d", cost, l.limit)
	}
	for {
		l.mu.Lock()
		now := l.now()
		for len(l.spent) > 0 && !l.spent[0].at.Add(l.window).After(now) {
			l.total -= l.spent[0].cost
			l.spent = l.spent[1:]
		}
		if l.total+cost <= l.limit {
			l.spent = append(l.spent, spend{at: now, cost: cost})
			l.total += cost
			l.mu.Unlock()
			return nil
		}
		// Wait until enough of the oldest transactions leave the window.
		free, wait := l.limit-l.total, time.Duration(0)
		for _, s := range l.spent {
			free += s.cost
			wait = s.at.Add(l.window).Sub(now)
			if free >= cost {
				break
			}
		}
		l.mu.Unlock()
		if err := l.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
