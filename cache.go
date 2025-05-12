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
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Default size of the metadata headers.
const defaultUnitSize = 16

// runtimeStart holds start time of the cache, to calculate expiration time
// with monotonic clock instead of relying on system wall clock.
var runtimeStart = time.Now()

var (
	ErrDuration  = errors.New("non-positive duration")
	ErrExists    = errors.New("already exists")
	ErrExpired   = errors.New("is expired")
	ErrNotExists = errors.New("does not exist")
	ErrMaxSize   = errors.New("max size limit")
	ErrMaxLength = errors.New("max data limit")
	ErrNotInt    = errors.New("data type is not integer")
)

type unit[T any] struct {
	Data T `json:"data"`

	Exp  uint64 `json:"exp"`
	Size uint64 `json:"size"`
}

func (u unit[T]) revive(lt uint64) unit[T] {
	u.Exp = uint64(time.Since(runtimeStart).Nanoseconds()) + lt
	return u
}

func (u *unit[T]) expired() bool {
	return u.Exp != 0 && time.Since(runtimeStart).Nanoseconds() >= int64(u.Exp)
}

type unitError struct {
	k   string
	err error
}

func (ue *unitError) Error() string {
	return fmt.Sprintf("key %q: %s", ue.k, ue.err)
}

func (ue *unitError) Unwrap() error { return ue.err }

type Cache[T any] struct {
	mu sync.RWMutex

	units map[string]unit[T]
	size  uint64
	opts  *cacheOpts
	j     *janitor
}

// New creates new Cache instance with given options.
func New[T any](opts ...cacheOptFn) *Cache[T] {
	co := defaultCacheOpts()
	for _, fn := range opts {
		fn(co)
	}

	c := &Cache[T]{units: make(map[string]unit[T]), opts: co}

	c.j = hireJanitor(co.cleanupInterval)
	if c.j != nil {
		go c.inviteJanitor()
	}

	return c
}

// StopCleaning stops current janitor if it was set. This function waits
// until janitor is unlocked if it is in cleaning progress.
func (c *Cache[T]) StopCleaning() {
	if c.j != nil {
		c.mu.Lock()
		c.j.fireJanitor()
		c.j = nil
		c.mu.Unlock()
	}
}

// OrderCleaning stops current janitor if it was set and starts a new one with
// cache default cleanup interval.
func (c *Cache[T]) OrderCleaning() {
	c.mu.Lock()
	if c.j != nil {
		c.j.fireJanitor()
	}

	c.j = hireJanitor(c.opts.cleanupInterval)
	c.mu.Unlock()

	go c.inviteJanitor()
}

// RescheduleCleaning stops current janitor if it was set, updates cache default
// cleanup interval with given duration and starts a new janitor.
func (c *Cache[T]) RescheduleCleaning(d time.Duration) error {
	if d <= 0 {
		return ErrDuration
	}

	c.mu.Lock()
	c.opts.cleanupInterval = d

	if c.j != nil {
		c.j.fireJanitor()
	}

	c.j = hireJanitor(c.opts.cleanupInterval)
	c.mu.Unlock()

	go c.inviteJanitor()

	return nil
}

// ChangeJanitorOnEviction updates cache default options with new janitor expiried
// keys removal behavior. Allows to control if janitor should apply on eviction function
// even if it was set. Restart janitor if it's currently running.
func (c *Cache[T]) ChangeJanitorOnEviction(b bool) {
	c.StopCleaning()
	c.mu.Lock()
	c.opts.janitorWEviction = b
	c.j = hireJanitor(c.opts.cleanupInterval)
	c.mu.Unlock()

	go c.inviteJanitor()
}

// ChangeMaxSize updates cache default options with new cache max size in bytes.
func (c *Cache[T]) ChangeMaxSize(i uint64) error {
	c.mu.Lock()

	if c.size > i {
		c.mu.Unlock()
		return ErrMaxSize
	}

	c.opts.maxSize = i
	c.mu.Unlock()

	return nil
}

// ChangeMaxLength updates cache default options with new max number of keys.
// Returns [ErrMaxLength] if new value is lower than number of keys already in cache.
func (c *Cache[T]) ChangeMaxLength(ml uint64) error {
	c.mu.Lock()

	if len(c.units) > int(ml) {
		c.mu.Unlock()
		return ErrMaxLength
	}

	c.opts.maxLength = ml
	c.mu.Unlock()

	return nil
}

// ChangeDefaultLifeTime updates cache default options with new default lifetime for key.
// Does not affect keys already in cache.
func (c *Cache[T]) ChangeDefaultLifeTime(lt uint64) {
	c.mu.Lock()
	c.opts.defaultLifetime = lt
	c.mu.Unlock()
}

// ChangeSizeFn updates cache default options with new function to define data size.
// Does not affect keys already in cache.
func (c *Cache[T]) ChangeSizeFn(fn func(string, any) (uint64, error)) {
	c.mu.Lock()
	c.opts.getDataSize = fn
	c.mu.Unlock()
}

// ChangeOnEvictionFn updates cache default options with new function
// which runs when key is being cleaned after expiration.
// If janitor is cleaning cache, this function will wait until it
// finishes, before changing on eviction function.
func (c *Cache[T]) ChangeOnEvictionFn(fn func(string, any)) {
	c.StopCleaning()
	c.mu.Lock()
	c.opts.onEviction = fn
	c.j = hireJanitor(c.opts.cleanupInterval)
	c.mu.Unlock()

	go c.inviteJanitor()
}

// ChangeDisplacementPolicy updates cache options with new displacement.
func (c *Cache[T]) ChangeDisplacementPolicy(v bool) {
	c.mu.Lock()
	c.opts.displacement = v
	c.mu.Unlock()
}

// Get returns data of the given key. If key does not exist then
// [ErrNotExists] will be returned. If key is already expired, but
// was not yet cleaned, returns data and [ErrExpired] as error.
func (c *Cache[T]) Get(k string) (T, error) {
	u, ok := c.getUnit(k)
	if !ok {
		return nil, &unitError{k, ErrNotExists}
	}

	if u.expired() {
		return u.Data, &unitError{k, ErrExpired}
	}

	return u.Data, nil
}

func (c *Cache[T]) getUnit(k string) (unit[T], bool) {
	c.mu.RLock()
	u, ok := c.units[k]
	c.mu.RUnlock()

	return u, ok
}

// deleteOneRnd takes first random key from the underlaying map.
func (c *Cache[T]) deleteOneRnd() {
	for k, u := range c.units {
		c.delete(k, u)
		return
	}
}

func (c *Cache[T]) delete(k string, u unit[T]) {
	delete(c.units, k)
	if c.opts.maxSize != 0 {
		c.size -= u.Size
	}

	if c.opts.onEviction != nil {
		c.opts.onEviction(k, u.Data)
	}
}

// Scan scans current [Snapshot] of the cache data and returns key-value map if key
// contains given sub-string.
func (c *Cache[T]) Scan(sub string) map[string]T {
	return c.ScanFunc(func(s string) bool { return strings.Contains(s, sub) })
}

// ScanFunc scans current [Snapshot] of the cache and returns key-value map
// if given func returns true for a key.
func (c *Cache[T]) ScanFunc(fn func(string) bool) map[string]T {
	snap := c.Snapshot()
	res := make(map[string]T, 0)

	for k, v := range snap {
		if fn(k) {
			res[k] = v
		}
	}

	return res
}

// Set saves data in cache with given key and options.
// If key already exists, it will be removed before adding new value.
func (c *Cache[T]) Set(k string, a T, opts ...unitOptFn[T]) error {
		if len(c.units)+1 > int(c.opts.maxLength) {
			return ErrMaxLength
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
			s, err := c.opts.getDataSize(k, a)
			if err != nil {
				return &unitError{k, err}
			}

			u.Size = defaultUnitSize + s
		}

		if c.size+u.Size > c.opts.maxSize {
			return ErrMaxSize
		}

		c.size += u.Size
	}

	c.mu.Lock()
	c.units[k] = u
	c.mu.Unlock()

	return nil
}

// Add sets data in cache only if given key does not exist.
func (c *Cache[T]) Add(k string, a T, opts ...unitOptFn[T]) error {
	if _, ok := c.getUnit(k); ok {
		return &unitError{k, ErrExists}
	}

	return c.Set(k, a, opts...)
}

// Replace replaces data of the given key only if this key exists in cache
// and is not expired.
func (c *Cache[T]) Replace(k string, a T) error {
	u, ok := c.getUnit(k)
	if !ok {
		return &unitError{k, ErrNotExists}
	}

	if u.expired() {
		return &unitError{k, ErrExpired}
	}

	c.mu.Lock()
	u.Data = a
	c.units[k] = u
	c.mu.Unlock()

	return nil
}

// Rename renames old key with a new name only if given key exists in cache
// and is not expired.
func (c *Cache[T]) Rename(oldKey, newKey string) error {
	u, ok := c.getUnit(oldKey)
	if !ok {
		return &unitError{oldKey, ErrNotExists}
	}

	if u.expired() {
		return &unitError{oldKey, ErrExpired}
	}

	c.mu.Lock()
	c.units[newKey] = u
	c.delete(oldKey, u)
	c.mu.Unlock()

	return nil
}

// Remove removes key with given name from cache. Do nothing if key
// does not exist. Does not apply on eviction function even if it was set.
func (c *Cache[T]) Remove(k string) {
	u, ok := c.getUnit(k)
	if !ok {
		return
	}

	if c.opts.maxSize != 0 {
		c.size -= u.Size
	}

	c.mu.Lock()
	delete(c.units, k)
	c.mu.Unlock()
}

// RemoveAll removes all keys from cache.
// Does not apply on eviction function even if it was set.
// Runs GC to collect released memory.
func (c *Cache[T]) RemoveAll() {
	c.mu.Lock()
	clear(c.units)
	c.size = 0
	c.mu.Unlock()
	runtime.GC()
}

// RemoveExpired removes only expired keys. Does not apply on eviction
// function even if it was set.
func (c *Cache[T]) RemoveExpired() {
	c.mu.Lock()
	if len(c.units) == 0 {
		c.mu.Unlock()
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
	c.mu.Unlock()
}

// Delete removes key with given name from cache. Do nothing if key
// does not exist. Applies on eviction function if it was set.
func (c *Cache[T]) Delete(k string) {
	if u, ok := c.getUnit(k); ok {
		c.mu.Lock()
		c.delete(k, u)
		c.mu.Unlock()
	}
}

// DeleteAll removes all keys from cache applying on eviction function
// if it was set.
func (c *Cache[T]) DeleteAll() {
	c.mu.Lock()

	for k, u := range c.units {
		if c.opts.onEviction != nil {
			c.opts.onEviction(k, u.Data)
		}
	}

	clear(c.units)
	c.size = 0
	c.mu.Unlock()
}

// DeleteExpired removes only expired keys applying on eviction function
// if it was set.
func (c *Cache[T]) DeleteExpired() {
	c.mu.Lock()
	if len(c.units) == 0 {
		c.mu.Unlock()
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
	c.mu.Unlock()
}

// Alive creates copy of the cache with not expired keys data.
func (c *Cache[T]) Alive() map[string]T {
	c.mu.Lock()

	m := make(map[string]T, len(c.units))

	for k, u := range c.units {
		if !u.expired() {
			m[k] = u.Data
		}
	}

	c.mu.Unlock()

	return m
}

// Snapshot creates copy of the cache with all keys data.
func (c *Cache[T]) Snapshot() map[string]T {
	c.mu.Lock()

	m := make(map[string]T, len(c.units))

	for k, u := range c.units {
		m[k] = u.Data
	}

	c.mu.Unlock()

	return m
}

// Length returns number of keys in cache.
func (c *Cache[T]) Length() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.units)
}

// Size returns current size of the cache in bytes.
func (c *Cache[T]) Size() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.size
}

// Revive prolongs lifetime of the key with default value from cache options.
func (c *Cache[T]) Revive(k string) error {
	u, ok := c.getUnit(k)
	if !ok {
		return &unitError{k, ErrNotExists}
	}

	c.mu.Lock()
	c.units[k] = u.revive(c.opts.defaultLifetime)
	c.mu.Unlock()

	return nil
}

// ReviveUntil prolongs lifetime of the key with specified value.
func (c *Cache[T]) ReviveUntil(k string, lt uint64) error {
	u, ok := c.getUnit(k)
	if !ok {
		return &unitError{k, ErrNotExists}
	}

	c.mu.Lock()
	c.units[k] = u.revive(lt)
	c.mu.Unlock()

	return nil
}

type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// Increment increments data of the given key with n. Returns error if key
// does not exist, was expired or if type assertion to [Integer] has failed.
func Increment[T Integer](c *Cache[T], k string, n T) (T, error) {
	var i T

	u, ok := c.getUnit(k)
	if !ok {
		return i, &unitError{k, ErrNotExists}
	}

	if u.expired() {
		return i, &unitError{k, ErrExpired}
	}

	i, ok = any(u.Data).(T)
	if !ok {
		return i, fmt.Errorf("%w; got type %T", &unitError{k, ErrNotInt}, i)
	}

	i += n
	u.Data = i

	c.mu.Lock()
	c.units[k] = u
	c.mu.Unlock()

	return i, nil
}

// Decrement decrements data of the given key with n. Returns error if key
// does not exist, was expired or if type assertion to [Integer] has failed.
func Decrement[T Integer](c *Cache[T], k string, n T) (T, error) {
	var i T

	u, ok := c.getUnit(k)
	if !ok {
		return i, &unitError{k, ErrNotExists}
	}

	if u.expired() {
		return i, &unitError{k, ErrExpired}
	}

	i, ok = any(u.Data).(T)
	if !ok {
		return i, fmt.Errorf("%w; got type %T", &unitError{k, ErrNotInt}, i)
	}

	i -= n
	u.Data = i

	c.mu.Lock()
	c.units[k] = u
	c.mu.Unlock()

	return i, nil
}

// Save dumps cache into the given [io.Writer] with json marshaller.
func (c *Cache[T]) Save(w io.Writer) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := json.MarshalIndent(c.units, "", "  ")
	if err != nil {
		return err
	}

	_, err = w.Write(data)

	return err
}

// Load restore cache from the given [io.Reader] with json unmarshaller.
func (c *Cache[T]) Load(r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	var data map[string]unit[T]

	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}

	c.mu.Lock()

	for k, u := range data {
		c.units[k] = u
	}

	c.mu.Unlock()

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
func (c *Cache[T]) Stats() *Stats {
	c.mu.Lock()
	s := &Stats{
		c.opts.cleanupInterval,
		c.opts.defaultLifetime,
		len(c.units),
		c.opts.maxLength,
		c.opts.maxSize,
		c.size,
	}
	c.mu.Unlock()

	return s
}
