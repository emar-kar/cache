package cache

import (
	"bytes"
	"encoding/gob"
	"time"
)

type cacheOpts struct {
	getDataSize      func(string, any) (uint64, error)
	onEviction       func(string, any)
	cleanupInterval  time.Duration
	defaultLifetime  uint64
	maxLength        uint64
	maxSize          uint64
	displacement     bool
	janitorWEviction bool
}

func defaultCacheOpts() *cacheOpts {
	return &cacheOpts{
		getDataSize:      getSizeOf,
		cleanupInterval:  time.Duration(-1),
		janitorWEviction: true,
	}
}

func getSizeOf(key string, data any) (uint64, error) {
	b := new(bytes.Buffer)
	if err := gob.NewEncoder(b).Encode(data); err != nil {
		return 0, err
	}

	return uint64(b.Len() + len(key)), nil
}

type cacheOptFn func(*cacheOpts)

// WithDisplacement allows displacement of variables in cache.
// In case if adding new value will exceed max size or max length
// of the cache, random value will be removed to free up the space.
func WithDisplacement(co *cacheOpts) {
	co.displacement = true
}

// WithDefaultLifetime sets default lifetime for key in cache.
func WithDefaultLifetime(lt uint64) cacheOptFn {
	return func(co *cacheOpts) { co.defaultLifetime = lt }
}

// WithCleanupInteval sets interval for janitor to clean expired keys.
func WithCleanupInteval(d time.Duration) cacheOptFn {
	return func(co *cacheOpts) {
		if d > 0 {
			co.cleanupInterval = d
		}
	}
}

// WithMaxUnits sets maxixum number of keys, which can be stored in cache.
func WithMaxUnits(mu uint64) cacheOptFn {
	return func(co *cacheOpts) { co.maxLength = mu }
}

// WithMaxSize sets maximum cache data size in bytes.
func WithMaxSize(ms uint64) cacheOptFn {
	return func(co *cacheOpts) { co.maxSize = ms }
}

// WithOnEviction sets custom function which is applied when key is being
// deleted from cache.
//
// Deprecated: use [WithOnEvictionFn] instead.
func WithOnEviction(fn func(string, any)) cacheOptFn {
	return WithOnEvictionFn(fn)
}

// WithOnEvictionFn sets custom function which is applied when key is being
// deleted from cache.
func WithOnEvictionFn(fn func(string, any)) cacheOptFn {
	return func(co *cacheOpts) { co.onEviction = fn }
}

// WithoutJanitorEviction sets janitor to clean expired keys without applying
// on eviction function even if it was set.
func WithoutJanitorEviction(co *cacheOpts) { co.janitorWEviction = false }

// WithDataSize sets function which defines data size.
//
// Deprecated: use [WithDataSizeFn] instead.
func WithDataSize(fn func(string, any) (uint64, error)) cacheOptFn {
	return WithDataSizeFn(fn)
}

// WithDataSizeFn sets function which defines data size.
func WithDataSizeFn(fn func(string, any) (uint64, error)) cacheOptFn {
	return func(co *cacheOpts) {
		co.getDataSize = fn
	}
}

type unitOptFn func(unit) unit

// WithLifetime sets custom lifetime for cache key.
func WithLifetime(lt uint64) unitOptFn {
	return func(u unit) unit {
		u.Exp = lt
		return u
	}
}

// WithSize sets custom size for key data. If set, cache will ignore
// size calculation and use passed value. Adds default metadata size.
func WithSize(s uint64) unitOptFn {
	return func(u unit) unit {
		u.Size = s + defaultUnitSize
		return u
	}
}
