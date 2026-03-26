# go-cache

A generic, thread-safe in-memory key-value cache for Go. Keys and values are
fully type-parameterised. Expiry is tracked via a monotonic clock, making it
immune to wall-clock adjustments.

```go
import "github.com/emar-kar/cache/v2"
```

---

## Quick start

```go
c := cache.New[string, string]()

// Store a value.
if err := c.Set("greeting", "hello"); err != nil {
    log.Fatal(err)
}

// Retrieve it.
v, err := c.Get("greeting")
fmt.Println(v) // hello
```

---

## Creating a cache

```go
func New[K comparable, T any](opts ...option[K, T]) *Cache[K, T]
```

All options are optional. Combine them freely:

```go
c := cache.New[string, any](
    cache.WithDefaultTTL[string, any](10 * time.Minute), // Keys expire after 10 min.
    cache.WithMaxEntries[string, any](10_000),            // At most 10 000 keys.
    cache.WithMaxSize[string, any](256 * 1024 * 1024),   // At most 256 MiB of data.
    cache.WithCapacity[string, any](10_000),              // Pre-allocate map for 10 000 entries.
    cache.WithCleanupInterval[string, any](time.Minute), // Janitor runs every minute.
    cache.WithOnEvictionFunc[string, any](func(k string, v any) {
        log.Printf("evicted %q", k)
    }),
    cache.WithDisplacement[string, any], // Allow random eviction when full.
)
```

### Cache options

| Option | Description |
|---|---|
| `WithDefaultTTL(d)` | Default TTL for keys; 0 means never expire |
| `WithMaxEntries(n)` | Maximum number of keys |
| `WithMaxSize(bytes)` | Maximum total size in bytes (requires a size function or per-entry `WithSize`) |
| `WithCapacity(n)` | Pre-allocate the internal map for `n` entries, reducing map growth allocations |
| `WithCleanupInterval(d)` | How often the janitor removes expired keys |
| `WithOnEvictionFunc(fn)` | Called whenever a key is evicted (by `Delete`, `DeleteAll`, `DeleteExpired`, or displacement) |
| `WithoutJanitorEviction` | Janitor cleans silently — `OnEvictionFunc` is not called during scheduled cleanup |
| `WithDisplacement` | When at capacity, evict a random entry instead of returning `ErrMaxLength`/`ErrMaxSize` |
| `WithSizeFunc(fn)` | Custom function `func(K, T) uint64` for fine-grained size accounting |

---

## Entry options

Pass per-entry options as variadic arguments to `Set` and `Add`:

```go
c.Set("session", token,
    cache.WithTTL[string](30 * time.Minute), // Expires in 30 min, overrides cache default.
    cache.WithSize[string](256),             // Explicit size in bytes, bypasses the size function.
)
```

| Option | Description |
|---|---|
| `WithTTL[T](d)` | Per-entry TTL, overrides the cache default |
| `WithSize[T](bytes)` | Explicit size in bytes, bypasses the size function |

---

## Core operations

### Set

Stores `v` under `k`. If `k` already exists its old entry is silently
replaced (no eviction callback). Returns `ErrMaxLength` or `ErrMaxSize` if
the cache is at capacity and displacement is disabled.

```go
err := c.Set("key", value)
err := c.Set("key", value, cache.WithTTL[string](5*time.Minute))
```

### Add

Like `Set`, but only inserts when `k` is absent or expired. Returns
`ErrExist` for live keys.

```go
err := c.Add("key", value)
```

### Get

Returns the value and any error (`ErrNotExist` / `ErrExpired`). An expired
entry is still returned together with `ErrExpired` — it is not removed.

```go
v, err := c.Get("key")
if errors.Is(err, cache.ErrExpired) {
    // entry exists but has passed its TTL
}
```

### Replace

Replaces the stored value for a live key without changing its TTL or size.
Returns `ErrNotExist` or `ErrExpired` if the key cannot be replaced in-place.

```go
err := c.Replace("key", newValue)
```

### Rename

Renames `oldKey` to `newKey`. Returns `ErrNotExist` if the source is absent
or `ErrExist` if the destination already holds an entry. If `oldKey` is
expired it is revived with the cache default TTL.

```go
err := c.Rename("old", "new")
```

---

## Removal

| Method | Eviction callback | What is removed |
|---|---|---|
| `Delete(k)` | ✅ | One key |
| `DeleteAll()` | ✅ | All keys |
| `DeleteExpired()` | ✅ | Expired keys only |
| `Remove(k)` | ❌ | One key |
| `RemoveAll()` | ❌ | All keys |
| `RemoveExpired()` | ❌ | Expired keys only |

```go
c.Delete("session")        // Eviction with callback.
c.Remove("session")        // Silent, no callback.
c.DeleteExpired()          // Removes all past-TTL keys with callback.
```

---

## Expiry & introspection

### Alive

Returns a copy of all **live** (non-expired) entries.

```go
live := c.Alive() // map[K]T
```

### Snapshot

Returns a copy of **all** entries, including those that are expired but not
yet cleaned up.

```go
all := c.Snapshot() // map[K]T
```

### ScanFunc

Returns live entries matching a predicate on the key and value.

```go
c := cache.New[string, string]()

results := c.ScanFunc(func(k string, v string) bool {
    return strings.HasPrefix(k, "session:") && v != ""
})
```

### Revive

Resets a key's TTL so it stops being expired.

```go
c.Revive("key")                       // uses cache default TTL
c.ReviveFor("key", 15*time.Minute)    // explicit duration
```

---

## Increment / Decrement

Two package-level functions operate on any integer type stored in the cache.

```go
type Integer interface {
    ~int | ~int8 | ~int16 | ~int32 | ~int64 |
        ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

newVal, err := cache.Increment(c, "hits", 1)
newVal, err := cache.Decrement(c, "stock", 5)
```

Both return `ErrNotExist` if the key is absent or `ErrExpired` if it has
expired.

---

## Size tracking

When `WithMaxSize` is set, the cache charges each entry against a byte
budget. By default the budget is estimated using `unsafe.Sizeof` (i.e. the
static header size of the key and value types, without following pointers).

For string values or complex structs, supply a more accurate function:

```go
c := cache.New[string, []byte](
    cache.WithMaxSize[string, []byte](64 * 1024 * 1024), // 64 MiB.
    cache.WithSizeFunc[string, []byte](func(k string, v []byte) uint64 {
        return uint64(len(k) + len(v))
    }),
)
```

You can also bypass the function on a per-entry basis with `WithSize[T](n)`.

---

## Eviction callback — async example

The eviction callback is called synchronously inside the write lock.
To fire-and-forget, launch a goroutine inside the callback:

```go
c := cache.New[string, any](
    cache.WithOnEvictionFunc[string, any](func(k string, v any) {
        go func() {
            // async cleanup, logging, etc.
        }()
    }),
)
```

---

## Janitor

The janitor is a background goroutine that periodically removes expired keys.
It is only started when `WithCleanupInterval` is provided.

```go
c.StopCleaning()                          // Pause the janitor.
c.OrderCleaning()                         // Starts the cleanup.
c.RescheduleCleaning(5 * time.Minute)     // Change interval and restart.
c.ChangeJanitorOnEviction(false)          // Disable callbacks during cleanup.
```

By default (`WithoutJanitorEviction` is not set), the eviction callback fires
during scheduled cleanup. Disable it for silent background sweeps.

---

## Dynamic configuration

All limits and callbacks can be changed after construction:

```go
c.ChangeDefaultTTL(20 * time.Minute)
c.ChangeMaxLength(50_000)           // Returns ErrMaxLength if current count exceeds new limit; 0 disables.
c.ChangeMaxSize(512 * 1024 * 1024)  // Returns ErrMaxSize if current usage exceeds new limit; 0 disables.
c.ChangeSizeFunc(myFunc)
c.ChangeOnEvictionFunc(myHandler)
c.ChangeDisplacementPolicy(true)
```

---

## Statistics

```go
stats := c.Stats() // returns *Stats, safe for json.Marshal

// *Stats fields:
// CleanupInterval time.Duration `json:"cleanup_interval"` // -1 = no janitor
// DefaultLifetime time.Duration `json:"default_lifetime"`
// MaxLength       uint64        `json:"max_length"`
// MaxSize         uint64        `json:"max_size"`
// CurrentSize     uint64        `json:"current_size"`
// CurrentLength   int           `json:"current_length"`

c.Len()   // current number of entries (all, including expired)
c.Size()  // current tracked size in bytes
```

---

## Save / Load (JSON persistence)

The cache can be serialised to JSON and restored. Expiry offsets are stored
as monotonic durations from a fixed process-start epoch, so they cannot be
faithfully restored across restarts — entries will appear as already-expired
after a reload.

```go
// Save
f, _ := os.Create("dump.json")
if err := c.Save(f); err != nil { ... }
f.Close()

// Load into a fresh (or existing) cache
f, _ := os.Open("dump.json")
defer f.Close()
if err := c.Load(f); err != nil { ... }
```

Structs with unexported fields cannot be roundtripped via `encoding/json`.
Use types with exported fields and `json` tags, or load the raw `map[string]any`
and type-assert manually.

---

## Error reference

| Error | When returned |
|---|---|
| `ErrNotExist` | Key is not in the cache |
| `ErrExpired` | Key exists but has passed its TTL |
| `ErrExist` | Key already exists (returned by `Add`) |
| `ErrMaxLength` | Cache is at entry capacity and displacement is off |
| `ErrMaxSize` | Cache is at byte capacity and displacement is off |

---
