package cache

import (
	"time"
)

type janitor struct {
	t    *time.Ticker
	stop chan struct{}
	done chan struct{}
}

func (c *Cache) inviteJanitor() {
	for {
		if c.opts.janitorWEviction {
			c.DeleteExpired()
		} else {
			c.RemoveExpired()
		}

		select {
		case <-c.j.t.C:
		case <-c.j.stop:
			close(c.j.done)
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
		done: make(chan struct{}, 1),
	}
}

func (j *janitor) fireJanitor() {
	j.t.Stop()
	close(j.stop)
	<-j.done
}
