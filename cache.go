// Package cache provides a generic, thread-safe in-memory key-value store
// backed by a Go map. It supports configurable expiration per entry (default
// or individual), an optional maximum number of entries, an optional maximum
// byte budget, and a janitor that periodically removes expired keys.
// Data can be persisted to and restored from JSON. Expiration is tracked via
// a monotonic clock offset so it is immune to wall-clock adjustments, but
// expiration state cannot be faithfully restored across process restarts.
// Cache usage is safe for concurrent access from multiple goroutines.
package cache

import (
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"sync"
	"time"
)

// runtimeStart holds start time of the cache, to calculate expiration time
// with monotonic clock instead of relying on system wall clock.
var runtimeStart = time.Now()

var (
	ErrDuration = errors.New("non-positive duration")
	ErrExist    = errors.New("already exists")
	// Deprecated: ErrExists exists for historical compatibility and will be
	// removed in a future version. Use [ErrExist] instead, which follows the
	// standard library naming convention (e.g. [os.ErrExist]).
	ErrExists   = ErrExist
	ErrExpired  = errors.New("is expired")
	ErrNotExist = errors.New("does not exist")
	// Deprecated: ErrNotExists exists for historical compatibility and will be
	// removed in a future version. Use [ErrNotExist] instead, which follows the
	// standard library naming convention (e.g. [os.ErrNotExist]).
	ErrNotExists = ErrNotExist
	ErrMaxSize   = errors.New("max size limit")
	ErrMaxLength = errors.New("max data limit")
	ErrNotInt    = errors.New("data type is not integer")
)

type Cache[K comparable, T any] struct {
	sync.RWMutex

	entries map[K]entry[T]
	size    uint64
	opts    *options[K, T]
	j       *janitor
}

// New creates new Cache instance with given options.
func New[K comparable, T any](opts ...option[K, T]) *Cache[K, T] {
	co := defaultCacheOpts[K, T]()
	for _, fn := range opts {
		fn(co)
	}

	c := &Cache[K, T]{entries: make(map[K]entry[T]), opts: co}
	c.hireJanitor()

	return c
}

// StopCleaning stops current janitor if it was set. This function waits
// until janitor is unlocked if it is in cleaning progress.
func (c *Cache[K, T]) StopCleaning() {
	c.Lock()
	c.fireJanitor()
	c.Unlock()
}

// OrderCleaning stops current janitor if it was set and starts a new one with
// cache default cleanup interval.
func (c *Cache[K, T]) OrderCleaning() {
	c.Lock()
	c.fireJanitor()
	c.hireJanitor()
	c.Unlock()

}

// RescheduleCleaning stops current janitor if it was set, updates cache default
// cleanup interval with given duration and starts a new janitor.
func (c *Cache[K, T]) RescheduleCleaning(d time.Duration) error {
	if d <= 0 {
		return ErrDuration
	}

	c.Lock()
	c.opts.cleanupInterval = d
	c.fireJanitor()
	c.hireJanitor()
	c.Unlock()

	return nil
}

// ChangeJanitorOnEviction updates cache default options with new janitor expired
// keys removal behavior. Allows to control if janitor should apply on eviction function
// even if it was set. Restart janitor if it's currently running.
func (c *Cache[K, T]) ChangeJanitorOnEviction(b bool) {
	c.StopCleaning()
	c.Lock()
	c.opts.janitorWEviction = b
	c.hireJanitor()
	c.Unlock()

}

// ChangeMaxSize updates the maximum cache size in bytes.
// Returns [ErrMaxSize] if the current total size of tracked entries already
// exceeds i. Displacement is not applied; shrink the cache manually before
// calling if needed. Setting i to 0 disables the size limit.
// If i is non-zero, the tracked size counter is rebuilt from entries with
// known sizes; entries inserted without an explicit size or a configured size
// function will not be counted until they are next updated.
func (c *Cache[K, T]) ChangeMaxSize(i uint64) error {
	c.Lock()
	defer c.Unlock()

	if c.size > i {
		return ErrMaxSize
	}

	// Speculatively accumulate the new size counter without touching any
	// cache state, so a failure leaves c.size and c.opts.maxSize unchanged.
	var newSize uint64
	if i != 0 {
		for _, e := range c.entries {
			if e.Size == 0 {
				continue
			}
			if newSize+e.Size > i {
				return ErrMaxSize
			}
			newSize += e.Size
		}
	}

	c.opts.maxSize = i
	c.size = newSize

	return nil
}

// ChangeMaxLength updates the maximum number of keys allowed in the cache.
// Returns [ErrMaxLength] if the current number of entries already exceeds ml.
// Displacement is not applied when changing the limit; shrink the cache
// manually before calling this if needed.
func (c *Cache[K, T]) ChangeMaxLength(ml uint64) error {
	c.Lock()
	defer c.Unlock()

	if len(c.entries) > int(ml) {
		return ErrMaxLength
	}

	c.opts.maxLength = ml

	return nil
}

// ChangeDefaultLifeTime updates cache default options with new default lifetime for key.
// Does not affect keys already in cache.
//
// Deprecated: ChangeDefaultLifeTime exists for historical compatibility and will
// be removed in a future version. Use [Cache.ChangeDefaultTTL] instead, which
// accepts a [time.Duration] directly.
func (c *Cache[K, T]) ChangeDefaultLifeTime(lt uint64) {
	c.ChangeDefaultTTL(time.Duration(lt))
}

// ChangeDefaultTTL updates the default time-to-live for keys stored after
// this call. Existing keys are not affected; their expiry was set at insert
// time. A value of 0 means new keys will not expire.
func (c *Cache[K, T]) ChangeDefaultTTL(d time.Duration) {
	c.Lock()
	c.opts.defaultLifetime = d
	c.Unlock()
}

// ChangeSizeFn replaces the function used to calculate the size of a cache entry.
//
// Deprecated: ChangeSizeFn exists for historical compatibility and will be
// removed in a future version. Use [Cache.ChangeSizeFunc] instead.
func (c *Cache[K, T]) ChangeSizeFn(fn func(K, T) uint64) {
	c.ChangeSizeFunc(fn)
}

// ChangeSizeFunc replaces the function used to calculate the in-memory size of
// a cache entry (key + value, in bytes). The new function applies to entries
// inserted after this call; existing entries retain their stored size.
// Size calculation only runs when a maximum size limit is configured.
func (c *Cache[K, T]) ChangeSizeFunc(fn func(K, T) uint64) {
	c.Lock()
	c.opts.getDataSize = fn
	c.Unlock()
}

// ChangeDataSizeFn updates cache default options with new function to define data size.
// Does not affect keys already in cache.
//
// Deprecated: ChangeDataSizeFn exists for historical compatibility and will be
// removed in a future version. The error return value is ignored internally.
// Use [Cache.ChangeSizeFunc] instead, which accepts a function without an error return.
func (c *Cache[K, T]) ChangeDataSizeFn(fn func(K, T) (uint64, error)) {
	c.ChangeSizeFunc(func(k K, t T) uint64 {
		n, _ := fn(k, t)
		return n
	})
}

// ChangeOnEvictionFn updates the function called when a key is evicted from cache.
//
// Deprecated: ChangeOnEvictionFn exists for historical compatibility and will be
// removed in a future version. Use [Cache.ChangeOnEvictionFunc] instead.
func (c *Cache[K, T]) ChangeOnEvictionFn(fn func(K, T)) {
	c.ChangeOnEvictionFunc(fn)
}

// ChangeOnEvictionFunc updates the function called when a key is evicted from
// cache. Waits for any in-progress janitor run to finish before applying the change.
func (c *Cache[K, T]) ChangeOnEvictionFunc(fn func(K, T)) {
	c.StopCleaning()
	c.Lock()
	c.opts.onEviction = fn
	c.hireJanitor()
	c.Unlock()
}

// ChangeDisplacementPolicy updates cache options with new displacement.
func (c *Cache[K, T]) ChangeDisplacementPolicy(v bool) {
	c.Lock()
	c.opts.displacement = v
	c.Unlock()
}

// Get returns the data stored for k. Returns [ErrNotExist] wrapped in a
// [entryError] if the key is absent. If the key is present but has passed its
// expiration time and has not yet been cleaned, the data is still returned
// together with [ErrExpired].
func (c *Cache[K, T]) Get(k K) (T, error) {
	c.RLock()
	defer c.RUnlock()

	e, ok := c.entries[k]
	switch {
	case !ok:
		return *new(T), &entryError[K]{k, ErrNotExist}
	case e.expired():
		return e.Data, &entryError[K]{k, ErrExpired}
	default:
		return e.Data, nil
	}
}

// deleteOneRnd deletes one arbitrarily-chosen entry from the cache to free
// space during displacement. The eviction callback fires via [clean] if configured.
func (c *Cache[K, T]) deleteOneRnd() {
	for k, e := range c.entries {
		c.clean(k, e)
		return
	}
}

// clean removes entry k from the cache, adjusts the size counter, and fires
// the eviction callback if one has been configured. Must be called with the
// write lock held.
func (c *Cache[K, T]) clean(k K, e entry[T]) {
	delete(c.entries, k)
	if c.opts.maxSize != 0 {
		c.size -= e.Size
	}

	if c.opts.onEviction != nil {
		c.opts.onEviction(k, e.Data)
	}
}

// Scan is deprecated and will be removed in a future version. The
// string-substring semantics of Scan do not generalise to Cache[K comparable, T].
// Use [Cache.ScanFunc] with your own predicate instead.
//
// Deprecated: use [Cache.ScanFunc].
func (c *Cache[K, T]) Scan(_ K) map[K]T { return nil }

// ScanFunc returns all live (non-expired) entries for which fn returns true.
// Expired entries that have not yet been cleaned are excluded; use
// [Cache.Snapshot] if you need to inspect the full internal state.
func (c *Cache[K, T]) ScanFunc(fn func(K) bool) map[K]T {
	alive := c.Alive()
	res := make(map[K]T, 0)

	for k, v := range alive {
		if fn(k) {
			res[k] = v
		}
	}

	return res
}

// Set stores v under k. If k already exists its size is deducted from the
// size budget and the entry is removed silently (no eviction callback).
// The new entry is then inserted subject to the usual length and size limits.
// Use [Cache.Delete] before Set if you need the eviction callback to fire
// for the old value.
// Returns [ErrMaxLength] if the cache is full and displacement is disabled,
// or [ErrMaxSize] if the entry would exceed the size budget and displacement
// is disabled. When displacement is enabled a random existing entry is
// evicted (with callback) to make room.
func (c *Cache[K, T]) Set(k K, v T, opts ...entryOption[T]) error {
	c.Lock()
	defer c.Unlock()

	if e, ok := c.entries[k]; ok {
		delete(c.entries, k)
		if c.opts.maxSize != 0 {
			c.size -= e.Size
		}
	}

	return c.set(k, v, opts...)
}

// Add stores v under k only if k does not hold a live (non-expired) entry.
// If k exists and is not expired, returns [ErrExist].
// If k exists but has expired, the stale entry is discarded silently
// (no eviction callback) and v is inserted in its place.
// Subject to the same length/size limits as [Cache.Set].
func (c *Cache[K, T]) Add(k K, v T, opts ...entryOption[T]) error {
	c.Lock()
	defer c.Unlock()

	if e, ok := c.entries[k]; ok {
		if !e.expired() {
			return &entryError[K]{k, ErrExist}
		}

		delete(c.entries, k)
		if c.opts.maxSize != 0 {
			c.size -= e.Size
		}
	}

	return c.set(k, v, opts...)
}

// verifyLength enforces the maxLength constraint before a new entry is
// inserted. If the map is at capacity and displacement is on, one random
// entry is evicted to make room; otherwise [ErrMaxLength] is returned.
// Must be called with the write lock held.
func (c *Cache[K, T]) verifyLength() error {
	if c.opts.maxLength != 0 {
		if len(c.entries)+1 > int(c.opts.maxLength) {
			if !c.opts.displacement {
				return ErrMaxLength
			}

			c.deleteOneRnd()
		}
	}

	return nil
}

// set is the internal implementation of Set/Add. It must be called with the
// write lock already held. It creates the cache entry, calculates its size
// when a max-size limit is configured, converts the per-entry lifetime into
// an absolute monotonic expiry via revive, and then delegates to verifySize
// before storing the entry.
func (c *Cache[K, T]) set(k K, v T, opts ...entryOption[T]) error {
	if err := c.verifyLength(); err != nil {
		return err
	}

	e := entry[T]{Data: v, Exp: c.opts.defaultLifetime}
	for _, fn := range opts {
		e = fn(e)
	}

	if c.opts.maxSize != 0 && e.Size == 0 {
		e.Size = c.opts.getDataSize(k, v)
	}

	e.revive(e.Exp)

	if err := c.verifySize(&e); err != nil {
		return err
	}

	c.entries[k] = e

	return nil
}

// verifySize enforces the maxSize constraint before a new entry is stored.
// Skipped entirely when maxSize is 0 or e.Size is 0 (unknown size).
// When displacement is on, random entries are evicted until there is room;
// otherwise [ErrMaxSize] is returned. Increments c.size on success.
// Must be called with the write lock held.
func (c *Cache[K, T]) verifySize(e *entry[T]) error {
	if c.opts.maxSize != 0 && e.Size != 0 {
		if e.Size > c.opts.maxSize {
			return ErrMaxSize
		}

		if c.size+e.Size > c.opts.maxSize {
			if !c.opts.displacement {
				return ErrMaxSize
			}

			for c.size+e.Size > c.opts.maxSize {
				c.deleteOneRnd()
			}
		}

		c.size += e.Size
	}

	return nil
}

// Replace replaces the stored value for k without changing its expiry or
// size. Returns [ErrNotExist] if the key is absent, or [ErrExpired] if the
// key has passed its expiration time.
func (c *Cache[K, T]) Replace(k K, v T) error {
	c.Lock()
	defer c.Unlock()

	e, ok := c.entries[k]
	if !ok {
		return &entryError[K]{k, ErrNotExist}
	}

	if e.expired() {
		return &entryError[K]{k, ErrExpired}
	}

	e.Data = v
	c.entries[k] = e

	return nil
}

// Rename renames oldKey to newKey. Returns [ErrExist] if newKey already
// exists, or [ErrNotExist] if oldKey is absent. If oldKey is expired it is
// revived with the cache default TTL before being stored under newKey;
// if the default TTL is 0 the expired entry is renamed as-is (still expired).
func (c *Cache[K, T]) Rename(oldKey, newKey K) error {
	if oldKey == newKey {
		return nil
	}

	c.Lock()
	defer c.Unlock()

	if _, ok := c.entries[newKey]; ok {
		return &entryError[K]{newKey, ErrExist}
	}

	oldU, ok := c.entries[oldKey]
	if !ok {
		return &entryError[K]{oldKey, ErrNotExist}
	}

	if oldU.expired() {
		oldU.revive(c.opts.defaultLifetime)
	}

	c.entries[newKey] = oldU
	delete(c.entries, oldKey)

	return nil
}

// Remove removes key with given name from cache. Do nothing if key
// does not exist. Does not apply on eviction function even if it was set.
func (c *Cache[K, T]) Remove(k K) {
	c.Lock()
	defer c.Unlock()

	if e, ok := c.entries[k]; ok {
		if c.opts.maxSize != 0 {
			c.size -= e.Size
		}
		delete(c.entries, k)
	}
}

// RemoveAll removes all keys from cache.
// Does not apply on eviction function even if it was set.
// Runs GC to collect released memory.
func (c *Cache[K, T]) RemoveAll() {
	c.Lock()
	clear(c.entries)
	c.size = 0
	c.Unlock()
	runtime.GC()
}

// RemoveExpired removes only expired keys. Does not apply on eviction
// function even if it was set.
func (c *Cache[K, T]) RemoveExpired() {
	c.Lock()
	defer c.Unlock()

	if len(c.entries) == 0 {
		return
	}

	for k, e := range c.entries {
		if e.expired() {
			if c.opts.maxSize != 0 {
				c.size -= e.Size
			}

			delete(c.entries, k)
		}
	}
}

// Delete removes key with given name from cache. Do nothing if key
// does not exist. Applies on eviction function if it was set.
func (c *Cache[K, T]) Delete(k K) {
	c.Lock()
	defer c.Unlock()

	e, ok := c.entries[k]
	if ok {
		c.clean(k, e)
	}
}

// DeleteAll removes all keys from cache applying on eviction function
// if it was set.
func (c *Cache[K, T]) DeleteAll() {
	c.Lock()
	defer c.Unlock()

	for k, e := range c.entries {
		if c.opts.onEviction != nil {
			c.opts.onEviction(k, e.Data)
		}
	}

	clear(c.entries)
	c.size = 0
}

// DeleteExpired removes only expired keys applying on eviction function
// if it was set.
func (c *Cache[K, T]) DeleteExpired() {
	c.Lock()
	defer c.Unlock()

	if len(c.entries) == 0 {
		return
	}

	for k, e := range c.entries {
		if e.expired() {
			if c.opts.maxSize != 0 {
				c.size -= e.Size
			}

			delete(c.entries, k)

			if c.opts.onEviction != nil {
				c.opts.onEviction(k, e.Data)
			}
		}
	}
}

// Alive creates copy of the cache with not expired keys data.
func (c *Cache[K, T]) Alive() map[K]T {
	c.RLock()

	m := make(map[K]T, len(c.entries))

	for k, e := range c.entries {
		if !e.expired() {
			m[k] = e.Data
		}
	}

	c.RUnlock()

	return m
}

// Snapshot returns a shallow copy of all entries currently held in the cache,
// including entries that are expired but have not yet been cleaned by the
// janitor. Use [Cache.Alive] to get only live (non-expired) entries.
func (c *Cache[K, T]) Snapshot() map[K]T {
	c.RLock()

	m := make(map[K]T, len(c.entries))

	for k, e := range c.entries {
		m[k] = e.Data
	}

	c.RUnlock()

	return m
}

// Length returns the number of keys in the cache.
//
// Deprecated: Length exists for historical compatibility and will be
// removed in a future version. Use [Cache.Len] instead.
func (c *Cache[K, T]) Length() int {
	return c.Len()
}

// Len returns the number of keys currently in the cache.
func (c *Cache[K, T]) Len() int {
	c.RLock()
	defer c.RUnlock()

	return len(c.entries)
}

// Size returns current size of the cache in bytes.
func (c *Cache[K, T]) Size() uint64 {
	c.RLock()
	defer c.RUnlock()

	return c.size
}

// Revive resets the expiry of k to the cache default TTL, effectively
// giving the key a fresh lifetime from now. Returns [ErrNotExist] if k
// is absent. If the default TTL is 0 the key is left unchanged (no-op).
func (c *Cache[K, T]) Revive(k K) error {
	c.Lock()
	defer c.Unlock()

	if e, ok := c.entries[k]; ok {
		e.revive(c.opts.defaultLifetime)
		c.entries[k] = e

		return nil
	}

	return &entryError[K]{k, ErrNotExist}
}

// ReviveUntil prolongs lifetime of the key with specified value.
//
// Deprecated: ReviveUntil exists for historical compatibility and will be
// removed in a future version. Use [Cache.ReviveFor] instead, which accepts
// a [time.Duration] directly.
func (c *Cache[K, T]) ReviveUntil(k K, lt uint64) error {
	return c.ReviveFor(k, time.Duration(lt))
}

// ReviveFor resets the expiry of k to d from now. Returns [ErrNotExist] if k
// is absent. If d is 0 the key is left unchanged (no-op per [entry.revive]).
func (c *Cache[K, T]) ReviveFor(k K, d time.Duration) error {
	c.Lock()
	defer c.Unlock()

	if e, ok := c.entries[k]; ok {
		e.revive(d)
		c.entries[k] = e

		return nil
	}

	return &entryError[K]{k, ErrNotExist}
}

type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// Increment increments data of the given key with n. Returns error if key
// does not exist, was expired or if type assertion to [Integer] has failed.
func Increment[K comparable, T Integer](c *Cache[K, T], k K, n T) (T, error) {
	var i T

	c.Lock()
	defer c.Unlock()

	e, ok := c.entries[k]
	if !ok {
		return i, &entryError[K]{k, ErrNotExist}
	}

	if e.expired() {
		return i, &entryError[K]{k, ErrExpired}
	}

	e.Data += n
	c.entries[k] = e

	return e.Data, nil
}

// Decrement decrements data of the given key with n. Returns error if key
// does not exist, was expired or if type assertion to [Integer] has failed.
func Decrement[K comparable, T Integer](c *Cache[K, T], k K, n T) (T, error) {
	var i T

	c.Lock()
	defer c.Unlock()

	e, ok := c.entries[k]
	if !ok {
		return i, &entryError[K]{k, ErrNotExist}
	}

	if e.expired() {
		return i, &entryError[K]{k, ErrExpired}
	}

	e.Data -= n
	c.entries[k] = e

	return e.Data, nil
}

// Save serialises the current cache contents to w as indented JSON.
//
// Consider using [Cache.Snapshot] instead: it returns a plain map[K]T
// that you can encode with any serialiser of your choice, re-insert with
// explicit TTLs, and avoids the limitations listed below.
//
// Known limitations:
//   - Serialisation uses encoding/json, so T must be JSON-marshallable.
//     Values containing functions, channels, or unexported fields will
//     produce an error or silently lose data. Maps with non-string keys
//     are also not supported by the standard JSON encoder.
//   - The expiry field of each entry is a monotonic offset relative to
//     the current process start and is not meaningful across restarts.
//     See [Cache.Load] for how expiry is handled on restore.
func (c *Cache[K, T]) Save(w io.Writer) error {
	c.RLock()
	defer c.RUnlock()

	data, err := json.MarshalIndent(c.entries, "", "  ")
	if err != nil {
		return err
	}

	_, err = w.Write(data)

	return err
}

// Load restores cache entries from r, which must contain JSON produced by
// [Cache.Save]. Each entry is inserted via the normal set path so that
// maxLength and maxSize limits (including displacement) are respected.
//
// Consider using [Cache.Snapshot] with a custom encoder and [Cache.Set]
// instead: it gives full control over serialisation format and per-entry TTLs.
//
// Known limitations:
//   - Expiry is not faithfully restored. The saved offset is relative to a
//     different process start and cannot be reused. All loaded entries
//     receive a fresh TTL from the cache default ([WithDefaultTTL]). Entries
//     without a default TTL are loaded as non-expiring.
//   - The saved size is reused when non-zero; otherwise the configured size
//     function recalculates it. If T was saved with a custom size function
//     that is no longer configured, sizes may differ.
//   - JSON deserialisation is subject to the same type constraints as [Cache.Save].
func (c *Cache[K, T]) Load(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	var data map[K]entry[T]

	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}

	c.Lock()
	defer c.Unlock()

	for k, e := range data {
		if err := c.set(k, e.Data, WithSize[T](e.Size)); err != nil {
			return err
		}
	}

	return nil
}

// Stats represents current cache statistics.
type Stats struct {
	CleanupInterval time.Duration
	DefaultLifetime time.Duration
	MaxLength       uint64
	MaxSize         uint64
	CurrentSize     uint64
	CurrentLength   int
}

// Stats gets current cache state.
func (c *Cache[K, T]) Stats() *Stats {
	c.RLock()
	defer c.RUnlock()

	return &Stats{
		c.opts.cleanupInterval,
		c.opts.defaultLifetime,
		c.opts.maxLength,
		c.opts.maxSize,
		c.size,
		len(c.entries),
	}
}
