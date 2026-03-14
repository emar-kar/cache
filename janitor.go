package cache

import (
	"time"
)

// janitor holds the ticker and stop channel used to drive the background
// cleanup goroutine. It is created by hireJanitor and invalidated by
// fireJanitor.
type janitor struct {
	t    *time.Ticker
	stop chan struct{}
}

// hireJanitor starts a background goroutine that periodically removes expired
// keys from the cache. It is a no-op when the configured cleanup interval is
// non-positive. hireJanitor must be called with the cache write lock held so
// that c.j is visible to all goroutines before any of them can call
// fireJanitor.
func (c *Cache[K, T]) hireJanitor() {
	if d := c.opts.cleanupInterval; d > 0 {
		c.j = &janitor{
			t:    time.NewTicker(d),
			stop: make(chan struct{}, 1),
		}

		// Capture both the janitor and the eviction flag as locals before
		// launching the goroutine. The goroutine must not read c.j or
		// c.opts.janitorWEviction directly: c.j may be overwritten (or set to
		// nil) by fireJanitor under the write lock, and janitorWEviction may
		// be changed by ChangeJanitorOnEviction — both would be concurrent,
		// unsynchronised reads from the goroutine's perspective and would
		// constitute data races. Using local copies makes the goroutine fully
		// independent of any future mutations to those fields.
		j := c.j
		withEviction := c.opts.janitorWEviction

		go func() {
			for {
				if withEviction {
					c.DeleteExpired()
				} else {
					c.RemoveExpired()
				}

				select {
				case <-j.t.C:
				case <-j.stop:
					return
				}
			}
		}()
	}
}

// fireJanitor stops the current janitor goroutine and clears c.j. It is a
// no-op when no janitor is running. fireJanitor must be called with the cache
// write lock held.
func (c *Cache[K, T]) fireJanitor() {
	if c.j != nil {
		c.j.t.Stop()
		close(c.j.stop)
		c.j = nil
	}
}
