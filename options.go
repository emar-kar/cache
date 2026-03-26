package cache

import (
	"time"
	"unsafe"
)

type options[K comparable, T any] struct {
	getDataSize      func(K, T) uint64
	onEviction       func(K, T)
	cleanupInterval  time.Duration
	defaultLifetime  time.Duration
	maxLength        uint64
	maxSize          uint64
	initialCap       int
	displacement     bool
	janitorWEviction bool
}

func defaultCacheOpts[K comparable, T any]() *options[K, T] {
	return &options[K, T]{
		getDataSize:      getSizeOf[K, T],
		cleanupInterval:  time.Duration(-1),
		janitorWEviction: true,
	}
}

// getSizeOf returns the in-memory size of key and data using unsafe.Sizeof.
// It measures the static layout size of each value — the struct or scalar
// itself — without following pointers. For types like string, []byte, or map
// where the backing allocation is heap-allocated, this underestimates actual
// memory usage. Use [WithSizeFn] to provide a more accurate measurement for
// those types. Size is only calculated when a max-size limit is configured.
func getSizeOf[K comparable, T any](key K, data T) uint64 {
	return uint64(unsafe.Sizeof(key)) + uint64(unsafe.Sizeof(data))
}

type option[K comparable, T any] func(*options[K, T])

// WithDisplacement allows displacement of variables in cache.
// In case if adding new value will exceed max size or max length
// of the cache, random value will be removed to free up the space.
func WithDisplacement[K comparable, T any](co *options[K, T]) {
	co.displacement = true
}

// WithDefaultLifetime sets default lifetime for keys in cache.
//
// Deprecated: WithDefaultLifetime exists for historical compatibility and will
// be removed in a future version. Use [WithDefaultTTL] instead, which accepts
// a [time.Duration] directly.
func WithDefaultLifetime[K comparable, T any](lt uint64) option[K, T] {
	return func(co *options[K, T]) { co.defaultLifetime = time.Duration(lt) }
}

// WithDefaultTTL sets the default time-to-live for keys in cache.
// Keys inserted without an explicit [WithUnitTTL] option will expire after
// this duration. A value of 0 means keys never expire by default.
func WithDefaultTTL[K comparable, T any](d time.Duration) option[K, T] {
	return func(co *options[K, T]) { co.defaultLifetime = d }
}

// WithCleanupInteval sets interval for janitor to clean expired keys.
//
// Deprecated: WithCleanupInteval exists for historical compatibility and will
// be removed in a future version. Use [WithCleanupInterval] instead.
func WithCleanupInteval[K comparable, T any](d time.Duration) option[K, T] {
	return WithCleanupInterval[K, T](d)
}

// WithCleanupInterval sets the interval at which the janitor removes expired keys.
func WithCleanupInterval[K comparable, T any](d time.Duration) option[K, T] {
	return func(co *options[K, T]) {
		if d > 0 {
			co.cleanupInterval = d
		}
	}
}

// WithMaxUnits sets the maximum number of keys that can be stored in cache.
//
// Deprecated: WithMaxUnits exists for historical compatibility and will be
// removed in a future version. Use [WithMaxEntries] instead.
func WithMaxUnits[K comparable, T any](mu uint64) option[K, T] {
	return WithMaxEntries[K, T](mu)
}

// WithMaxEntries sets the maximum number of keys that can be stored in the cache.
func WithMaxEntries[K comparable, T any](n uint64) option[K, T] {
	return func(co *options[K, T]) { co.maxLength = n }
}

// WithMaxSize sets maximum cache data size in bytes.
func WithMaxSize[K comparable, T any](ms uint64) option[K, T] {
	return func(co *options[K, T]) { co.maxSize = ms }
}

// WithCapacity sets the initial capacity hint for the internal map.
// This pre-allocates space for n entries, avoiding map growth allocations
// when the approximate number of entries is known ahead of time.
func WithCapacity[K comparable, T any](n int) option[K, T] {
	return func(co *options[K, T]) { co.initialCap = n }
}

// WithOnEvictionFn sets the function called when a key is evicted from cache.
//
// Deprecated: WithOnEvictionFn exists for historical compatibility and will be
// removed in a future version. Use [WithOnEvictionFunc] instead.
func WithOnEvictionFn[K comparable, T any](fn func(K, T)) option[K, T] {
	return WithOnEvictionFunc(fn)
}

// WithOnEvictionFunc sets the function called when a key is evicted from cache.
func WithOnEvictionFunc[K comparable, T any](fn func(K, T)) option[K, T] {
	return func(co *options[K, T]) { co.onEviction = fn }
}

// WithoutJanitorEviction sets janitor to clean expired keys without applying
// on eviction function even if it was set.
func WithoutJanitorEviction[K comparable, T any](co *options[K, T]) { co.janitorWEviction = false }

// WithDataSizeFn sets function which defines data size.
//
// Deprecated: WithDataSizeFn exists for historical compatibility and will be
// removed in a future version. The error return value is ignored internally.
// Use [WithSizeFn] instead, which accepts a function without an error return.
func WithDataSizeFn[K comparable, T any](fn func(K, T) (uint64, error)) option[K, T] {
	return func(co *options[K, T]) {
		co.getDataSize = func(k K, t T) uint64 {
			n, _ := fn(k, t)
			return n
		}
	}
}

// WithSizeFn sets the function used to calculate the in-memory size of a cache entry.
//
// Deprecated: WithSizeFn exists for historical compatibility and will be
// removed in a future version. Use [WithSizeFunc] instead.
func WithSizeFn[K comparable, T any](fn func(K, T) uint64) option[K, T] {
	return WithSizeFunc(fn)
}

// WithSizeFunc sets the function used to calculate the in-memory size of a
// cache entry. The function receives the key and value and must return their
// combined size in bytes. The function is only called when a max-size limit is
// configured and the entry does not already have an explicit size set via [WithSize].
func WithSizeFunc[K comparable, T any](fn func(K, T) uint64) option[K, T] {
	return func(co *options[K, T]) { co.getDataSize = fn }
}

type entryOption[T any] func(entry[T]) entry[T]

// WithLifetime sets custom lifetime for cache key.
//
// Deprecated: WithLifetime exists for historical compatibility and will be
// removed in a future version. Use [WithTTL] instead, which accepts a
// [time.Duration] directly.
func WithLifetime[T any](lt uint64) entryOption[T] {
	return func(u entry[T]) entry[T] {
		u.Exp = time.Duration(lt)
		return u
	}
}

// WithTTL sets a custom time-to-live for a single cache key, overriding
// the cache default set by [WithDefaultTTL]. A value of 0 means the key
// never expires, regardless of the cache default.
func WithTTL[T any](d time.Duration) entryOption[T] {
	return func(u entry[T]) entry[T] {
		u.Exp = d
		return u
	}
}

// WithSize sets an explicit size in bytes for a single cache entry,
// bypassing automatic size calculation. When set, the cache will use this
// value directly and skip the size function. Use this when you know the
// exact memory cost of an entry (e.g. when loading persisted data).
func WithSize[T any](s uint64) entryOption[T] {
	return func(u entry[T]) entry[T] {
		u.Size = s
		return u
	}
}
