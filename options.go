package cache

import (
	"time"
	"unsafe"
)

type options[K comparable, T any] struct {
	getDataSize      func(K, T) uint64
	onEviction       func(K, T)
	cleanupInterval  time.Duration
	defaultLifetime  uint64
	maxLength        uint64
	maxSize          uint64
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

// getSizeOf returns the in-memory size of the key and data using unsafe.Sizeof.
// It reports the static layout size of the values — i.e. the size of the struct
// or scalar itself — but does not follow pointers, so for types like string or
// []byte the backing allocation is not counted. Use [WithDataSizeFn] when
// precise deep-size accounting is required.
func getSizeOf[K comparable, T any](key K, data T) uint64 {
	return uint64(unsafe.Sizeof(key)) + uint64(unsafe.Sizeof(data))
}

type cacheOptFn[K comparable, T any] func(*options[K, T])

// WithDisplacement allows displacement of variables in cache.
// In case if adding new value will exceed max size or max length
// of the cache, random value will be removed to free up the space.
func WithDisplacement[K comparable, T any](co *options[K, T]) {
	co.displacement = true
}

// WithDefaultLifetime sets default lifetime for key in cache.
func WithDefaultLifetime[K comparable, T any](lt uint64) cacheOptFn[K, T] {
	return func(co *options[K, T]) { co.defaultLifetime = lt }
}

// WithCleanupInteval sets interval for janitor to clean expired keys.
func WithCleanupInteval[K comparable, T any](d time.Duration) cacheOptFn[K, T] {
	return func(co *options[K, T]) {
		if d > 0 {
			co.cleanupInterval = d
		}
	}
}

// WithMaxUnits sets maxixum number of keys, which can be stored in cache.
func WithMaxUnits[K comparable, T any](mu uint64) cacheOptFn[K, T] {
	return func(co *options[K, T]) { co.maxLength = mu }
}

// WithMaxSize sets maximum cache data size in bytes.
func WithMaxSize[K comparable, T any](ms uint64) cacheOptFn[K, T] {
	return func(co *options[K, T]) { co.maxSize = ms }
}

// WithOnEvictionFn sets custom function which is applied when key is being
// deleted from cache.
func WithOnEvictionFn[K comparable, T any](fn func(K, T)) cacheOptFn[K, T] {
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
func WithDataSizeFn[K comparable, T any](fn func(K, T) (uint64, error)) cacheOptFn[K, T] {
	return func(co *options[K, T]) {
		co.getDataSize = func(k K, t T) uint64 {
			n, _ := fn(k, t)
			return n
		}
	}
}

// WithSizeFn sets the function used to calculate the in-memory size of a
// cache entry. The function receives the key and value and must return their
// combined size in bytes. Use this instead of the deprecated [WithDataSizeFn].
func WithSizeFn[K comparable, T any](fn func(K, T) uint64) cacheOptFn[K, T] {
	return func(co *options[K, T]) { co.getDataSize = fn }
}

type unitOptFn[T any] func(unit[T]) unit[T]

// WithLifetime sets custom lifetime for cache key.
func WithLifetime[T any](lt uint64) unitOptFn[T] {
	return func(u unit[T]) unit[T] {
		u.Exp = lt
		return u
	}
}

// WithSize sets custom size for key data. If set, cache will ignore
// size calculation and use passed value. Adds default metadata size.
func WithSize[T any](s uint64) unitOptFn[T] {
	return func(u unit[T]) unit[T] {
		u.Size = s
		return u
	}
}
