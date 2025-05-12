package cache

import (
	"time"
)

type janitor struct {
	t    *time.Ticker
	stop chan struct{}
}

func (c *Cache[T]) inviteJanitor() {
	for {
		if c.opts.janitorWEviction {
			c.DeleteExpired()
		} else {
			c.RemoveExpired()
		}

		select {
		case <-c.j.t.C:
		case <-c.j.stop:
			return
		}
	}
}

func hireJanitor(d time.Duration) *janitor {
	if d <= 0 {
		return nil
	}

	return &janitor{
		t:    time.NewTicker(d),
		stop: make(chan struct{}, 1),
	}
}

func (j *janitor) fireJanitor() {
	j.t.Stop()
	close(j.stop)
}
