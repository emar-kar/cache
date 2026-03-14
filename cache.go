// Package cache is a simple implementation of in-memory key-value storage
// based on golang map type. This package allows to setup various options,
// such as values expiration time (default, individual), max number of
// entries in cache, max byte size of data which can be stored in cache.
// Data can be dumped into json file and restored from it with all saved metadata.
// Cache usage is thread safe and it can be accessed from multiple goroutines.
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

	units map[K]unit[T]
	size  uint64
	opts  *options[K, T]
	j     *janitor
}

// New creates new Cache instance with given options.
func New[K comparable, T any](opts ...cacheOptFn[K, T]) *Cache[K, T] {
	co := defaultCacheOpts[K, T]()
	for _, fn := range opts {
		fn(co)
	}

	c := &Cache[K, T]{units: make(map[K]unit[T]), opts: co}
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

// ChangeMaxSize updates cache default options with new cache max size in bytes.
func (c *Cache[K, T]) ChangeMaxSize(i uint64) error {
	c.Lock()

	if c.size > i {
		c.Unlock()
		return ErrMaxSize
	}

	c.opts.maxSize = i
	c.Unlock()

	return nil
}

// ChangeMaxLength updates cache default options with new max number of keys.
// Returns [ErrMaxLength] if new value is lower than number of keys already in cache.
func (c *Cache[K, T]) ChangeMaxLength(ml uint64) error {
	c.Lock()

	if len(c.units) > int(ml) {
		c.Unlock()
		return ErrMaxLength
	}

	c.opts.maxLength = ml
	c.Unlock()

	return nil
}

// ChangeDefaultLifeTime updates cache default options with new default lifetime for key.
// Does not affect keys already in cache.
func (c *Cache[K, T]) ChangeDefaultLifeTime(lt uint64) {
	c.Lock()
	c.opts.defaultLifetime = lt
	c.Unlock()
}

// ChangeSizeFn updates cache default options with new function to define data size.
// Does not affect keys already in cache.
func (c *Cache[K, T]) ChangeSizeFn(fn func(K, T) uint64) {
	c.Lock()
	c.opts.getDataSize = fn
	c.Unlock()
}

// ChangeDataSizeFn updates cache default options with new function to define data size.
// Does not affect keys already in cache.
//
// Deprecated: ChangeDataSizeFn exists for historical compatibility and will be
// removed in a future version. The error return value is ignored internally.
// Use [Cache.ChangeSizeFn] instead, which accepts a function without an error return.
func (c *Cache[K, T]) ChangeDataSizeFn(fn func(K, T) (uint64, error)) {
	c.ChangeSizeFn(func(k K, t T) uint64 {
		n, _ := fn(k, t)
		return n
	})
}

// ChangeOnEvictionFn updates cache default options with new function
// which runs when key is being cleaned after expiration.
// If janitor is cleaning cache, this function will wait until it
// finishes, before changing on eviction function.
func (c *Cache[K, T]) ChangeOnEvictionFn(fn func(K, T)) {
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

// Get returns data of the given key. If key does not exist then
// [ErrNotExists] will be returned. If key is already expired, but
// was not yet cleaned, returns data and [ErrExpired] as error.
func (c *Cache[K, T]) Get(k K) (T, error) {
	u, ok := c.getUnit(k)
	switch {
	case !ok:
		return *new(T), &unitError[K]{k, ErrNotExist}
	case u.expired():
		return u.Data, &unitError[K]{k, ErrExpired}
	default:
		return u.Data, nil
	}
}

func (c *Cache[K, T]) getUnit(k K) (unit[T], bool) {
	c.RLock()
	u, ok := c.units[k]
	c.RUnlock()

	return u, ok
}

// deleteOneRnd takes first random key from the underlying map.
func (c *Cache[K, T]) deleteOneRnd() {
	for k, u := range c.units {
		c.delete(k, u)
		return
	}
}

func (c *Cache[K, T]) delete(k K, u unit[T]) {
	delete(c.units, k)
	if c.opts.maxSize != 0 {
		c.size -= u.Size
	}

	if c.opts.onEviction != nil {
		c.opts.onEviction(k, u.Data)
	}
}

// Scan is deprecated and will be removed in a future version. The
// string-substring semantics of Scan do not generalise to Cache[K comparable, T].
// Use [Cache.ScanFunc] with your own predicate instead.
//
// Deprecated: use [Cache.ScanFunc].
func (c *Cache[K, T]) Scan(_ K) map[K]T { return nil }

// ScanFunc scans current [Snapshot] of the cache and returns key-value map
// if given func returns true for a key.
func (c *Cache[K, T]) ScanFunc(fn func(K) bool) map[K]T {
	snap := c.Snapshot()
	res := make(map[K]T, 0)

	for k, v := range snap {
		if fn(k) {
			res[k] = v
		}
	}

	return res
}

// Set saves data in cache with given key and options.
// If key already exists, it will be removed before adding new value.
func (c *Cache[K, T]) Set(k K, a T, opts ...unitOptFn[T]) error {
	c.Lock()
	defer c.Unlock()

	return c.set(k, a, opts...)
}

// Add sets data in cache only if given key does not exist.
func (c *Cache[K, T]) Add(k K, a T, opts ...unitOptFn[T]) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.units[k]; ok {
		return &unitError[K]{k, ErrExist}
	}

	return c.set(k, a, opts...)
}

// set is the internal implementation of Set. It must only be called
// with the write lock already held.
func (c *Cache[K, T]) set(k K, a T, opts ...unitOptFn[T]) error {
	ou, ok := c.units[k]
	if ok {
		c.delete(k, ou)
	}

	if !ok && c.opts.maxLength != 0 {
		if len(c.units)+1 > int(c.opts.maxLength) {
			if !c.opts.displacement {
				return ErrMaxLength
			}

			c.deleteOneRnd()
		}
	}

	u := unit[T]{Data: a, Exp: c.opts.defaultLifetime}
	for _, fn := range opts {
		u = fn(u)
	}

	if u.Exp != 0 {
		u = u.revive(u.Exp)
	}

	if c.opts.maxSize != 0 {
		if u.Size == 0 {
			u.Size = c.opts.getDataSize(k, a)
		}

		if c.size+u.Size > c.opts.maxSize {
			if !c.opts.displacement {
				return ErrMaxSize
			}

			for c.size > c.opts.maxSize {
				c.deleteOneRnd()
			}
		}

		c.size += u.Size
	}

	c.units[k] = u

	return nil
}

// Replace replaces data of the given key only if this key exists in cache
// and is not expired.
func (c *Cache[K, T]) Replace(k K, a T) error {
	u, ok := c.getUnit(k)
	if !ok {
		return &unitError[K]{k, ErrNotExist}
	}

	if u.expired() {
		return &unitError[K]{k, ErrExpired}
	}

	c.Lock()
	u.Data = a
	c.units[k] = u
	c.Unlock()

	return nil
}

// Rename renames old key with a new name only if given key exists in cache
// and is not expired.
func (c *Cache[K, T]) Rename(oldKey, newKey K) error {
	u, ok := c.getUnit(oldKey)
	if !ok {
		return &unitError[K]{oldKey, ErrNotExist}
	}

	if u.expired() {
		return &unitError[K]{oldKey, ErrExpired}
	}

	c.Lock()
	c.units[newKey] = u
	c.delete(oldKey, u)
	c.Unlock()

	return nil
}

// Remove removes key with given name from cache. Do nothing if key
// does not exist. Does not apply on eviction function even if it was set.
func (c *Cache[K, T]) Remove(k K) {
	u, ok := c.getUnit(k)
	if !ok {
		return
	}

	if c.opts.maxSize != 0 {
		c.size -= u.Size
	}

	c.Lock()
	delete(c.units, k)
	c.Unlock()
}

// RemoveAll removes all keys from cache.
// Does not apply on eviction function even if it was set.
// Runs GC to collect released memory.
func (c *Cache[K, T]) RemoveAll() {
	c.Lock()
	clear(c.units)
	c.size = 0
	c.Unlock()
	runtime.GC()
}

// RemoveExpired removes only expired keys. Does not apply on eviction
// function even if it was set.
func (c *Cache[K, T]) RemoveExpired() {
	c.Lock()
	if len(c.units) == 0 {
		c.Unlock()
		return
	}

	for k, u := range c.units {
		if u.expired() {
			if c.opts.maxSize != 0 {
				c.size -= u.Size
			}

			delete(c.units, k)
		}
	}
	c.Unlock()
}

// Delete removes key with given name from cache. Do nothing if key
// does not exist. Applies on eviction function if it was set.
func (c *Cache[K, T]) Delete(k K) {
	if u, ok := c.getUnit(k); ok {
		c.Lock()
		c.delete(k, u)
		c.Unlock()
	}
}

// DeleteAll removes all keys from cache applying on eviction function
// if it was set.
func (c *Cache[K, T]) DeleteAll() {
	c.Lock()

	for k, u := range c.units {
		if c.opts.onEviction != nil {
			c.opts.onEviction(k, u.Data)
		}
	}

	clear(c.units)
	c.size = 0
	c.Unlock()
}

// DeleteExpired removes only expired keys applying on eviction function
// if it was set.
func (c *Cache[K, T]) DeleteExpired() {
	c.Lock()
	if len(c.units) == 0 {
		c.Unlock()
		return
	}

	for k, u := range c.units {
		if u.expired() {
			if c.opts.maxSize != 0 {
				c.size -= u.Size
			}

			delete(c.units, k)

			if c.opts.onEviction != nil {
				c.opts.onEviction(k, u.Data)
			}
		}
	}
	c.Unlock()
}

// Alive creates copy of the cache with not expired keys data.
func (c *Cache[K, T]) Alive() map[K]T {
	c.Lock()

	m := make(map[K]T, len(c.units))

	for k, u := range c.units {
		if !u.expired() {
			m[k] = u.Data
		}
	}

	c.Unlock()

	return m
}

// Snapshot creates copy of the cache with all keys data.
func (c *Cache[K, T]) Snapshot() map[K]T {
	c.Lock()

	m := make(map[K]T, len(c.units))

	for k, u := range c.units {
		m[k] = u.Data
	}

	c.Unlock()

	return m
}

// Length returns number of keys in cache.
func (c *Cache[K, T]) Length() int {
	c.Lock()
	defer c.Unlock()

	return len(c.units)
}

// Size returns current size of the cache in bytes.
func (c *Cache[K, T]) Size() uint64 {
	c.Lock()
	defer c.Unlock()

	return c.size
}

// Revive prolongs lifetime of the key with default value from cache options.
func (c *Cache[K, T]) Revive(k K) error {
	u, ok := c.getUnit(k)
	if !ok {
		return &unitError[K]{k, ErrNotExist}
	}

	c.Lock()
	c.units[k] = u.revive(c.opts.defaultLifetime)
	c.Unlock()

	return nil
}

// ReviveUntil prolongs lifetime of the key with specified value.
func (c *Cache[K, T]) ReviveUntil(k K, lt uint64) error {
	u, ok := c.getUnit(k)
	if !ok {
		return &unitError[K]{k, ErrNotExist}
	}

	c.Lock()
	c.units[k] = u.revive(lt)
	c.Unlock()

	return nil
}

type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// Increment increments data of the given key with n. Returns error if key
// does not exist, was expired or if type assertion to [Integer] has failed.
func Increment[K comparable, T Integer](c *Cache[K, T], k K, n T) (T, error) {
	var i T

	u, ok := c.getUnit(k)
	if !ok {
		return i, &unitError[K]{k, ErrNotExist}
	}

	if u.expired() {
		return i, &unitError[K]{k, ErrExpired}
	}
	u.Data += n

	c.Lock()
	c.units[k] = u
	c.Unlock()

	return u.Data, nil
}

// Decrement decrements data of the given key with n. Returns error if key
// does not exist, was expired or if type assertion to [Integer] has failed.
func Decrement[K comparable, T Integer](c *Cache[K, T], k K, n T) (T, error) {
	var i T

	u, ok := c.getUnit(k)
	if !ok {
		return i, &unitError[K]{k, ErrNotExist}
	}

	if u.expired() {
		return i, &unitError[K]{k, ErrExpired}
	}

	u.Data -= n

	c.Lock()
	c.units[k] = u
	c.Unlock()

	return u.Data, nil
}

// Save dumps cache into the given [io.Writer] with json marshaller.
func (c *Cache[K, T]) Save(w io.Writer) error {
	c.Lock()
	defer c.Unlock()

	data, err := json.MarshalIndent(c.units, "", "  ")
	if err != nil {
		return err
	}

	_, err = w.Write(data)

	return err
}

// Load restore cache from the given [io.Reader] with json unmarshaller.
func (c *Cache[K, T]) Load(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	var data map[K]unit[T]

	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}

	c.Lock()

	for k, u := range data {
		c.units[k] = u
	}

	c.Unlock()

	return nil
}

// Stats represents current cache statistics.
type Stats struct {
	CleanupInterval time.Duration
	DefaultLifetime uint64
	CurrentLength   int
	MaxLength       uint64
	MaxSize         uint64
	CurrentSize     uint64
}

// Stats gets current cache state.
func (c *Cache[K, T]) Stats() *Stats {
	c.Lock()
	s := &Stats{
		c.opts.cleanupInterval,
		c.opts.defaultLifetime,
		len(c.units),
		c.opts.maxLength,
		c.opts.maxSize,
		c.size,
	}
	c.Unlock()

	return s
}
