## Simple cache package

This package is a simple in-memory key-value storage based on golang map type. Check [docs](https://pkg.go.dev/github.com/emar-kar/cache) for more details.

Data is stored as `any` interface inside a unit struct, which carries additional metadata.

### Import

```go
import "github.com/emar-kar/cache"
```

### Cache options

* `WithDefaultLifetime` - sets default lifetime of every key added into the cache
* `WithCleanupInteval` - sets janitor run interval
* `WithMaxUnits` - sets max number of keys in cache
* `WithMaxSize` - sets max size of stored data in bytes
* `WithOnEvictionFn` - sets custom function which is triggered on key removal by janitor or manually
* `WithoutJanitorEviction` - defines janitor behavior if it should trigger on eviction function on clean up
* `WithDataSizeFn` - sets custom function to define size of the data

### Keys set options

* `WithLifetime` - sets custom lifetime for cache key
* `WithSize` - sets custom size for key data. If set, cache will ignore size calculation and use passed value. Adds default metadata size

In addition to global cache options, user can set individual lifetime per key or set data size to omit auto calculations.

### Available functionality

| Func | What it does |
|---|---|
| `Get` | Returns data of the given key |
| `Scan` | Scans current snapshot of the cache data and returns key-value map if key contains given sub-string |
| `ScanFunc` | Scans current snapshot of the cache and returns key-value map if given func returns true for a key |
| `Set` | Saves data in cache with given key and options. If key already exists it will be replaced without warnings |
| `Add` | Sets data in cache if given key does not exist |
| `Replace` | Replaces data of the given key only if this key exists in cache and is not expired |
| `Rename` | Renames old key with a new name only if given key exists in cache and is not expired |
| `Remove` | Removes key with given name from cache. Do nothing if key does not exist. Does not apply on eviction function even if it was set |
| `RemoveAll` | Removes all keys from cache. Does not apply on eviction function even if it was set. Runs GC to collect released memory |
| `RemoveExpired` | Removes only expired keys. Does not apply on eviction function even if it was set |
| `Delete` | Removes key with given name from cache. Do nothing if key does not exist. Applies on eviction function if it was set |
| `DeleteAll` | Removes all keys from cache applying on eviction function if it was set |
| `DeleteExpired` | Removes only expired keys applying on eviction function if it was set |
| `Alive` | Creates copy of the cache with not expired keys data |
| `Snapshot` | Creates copy of the cache with all keys data |
| `Revive` | Prolongs lifetime of the key with default value from cache options |
| `ReviveUntil` | Prolongs lifetime of the key with specified value | 
| `Length` | Returns number of keys in cache |
| `Size` | Returns current size of the cache in bytes |
| `ChangeMaxSize` | Updates cache default options with new cache max size in bytes |
| `ChangeMaxLength` | Updates cache default options with new max number of keys |
| `StopCleaning` | Stops current janitor if it was set. This function waits until janitor is unlocked if it is in cleaning progress |
| `ChangeDefaultLifeTime` | Updates cache default options with new default lifetime for key |
| `ChangeSizeFn` | Updates cache default options with new function to define data size |
| `ChangeOnEvictionFn` | Updates cache default options with new function which runs when key is being cleaned after expiration. If janitor is cleaning cache, this function will wait until it finishes, before changing on eviction function |
| `OrderCleaning` | Stops current janitor if it was set and starts a new one with cache default cleanup interval |
| `RescheduleCleaning` | Stops current janitor if it was set, updates cache default cleanup interval with given duration and starts a new janitor |
| `ChangeJanitorOnEviction` | Updates cache default options with new janitor expiried keys removal behavior. Allows to control if janitor should apply on eviction function even if it was set. Restart janitor if it's currently running |

### Increment/decrement values

Package has two exported functions, which recive `Cache` as first argument and support increment and decrement of the stored values by given N. It was done this way to support generics with **Integer** type:

```go
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}
```

```go
func Increment[T Integer](c *Cache, k string, n T) (T, error)

func Decrement[T Integer](c *Cache, k string, n T) (T, error)
```

Those functions will return error if key does not exist, was expired or it's data type assertion failed.

### Save/Load

Experimentally there is a support of marshal/unmarshal data to `json` format. Since data is stored as `any` interface, it can dump custom formats as well. Those features are experimental and might change in future. Check examples for more details.

```go
c := New(
    WithDefaultLifetime(uint64(time.Hour)),
    WithMaxSize(1028),
)

if err := c.Set("foo", "simple string"); err != nil {
    // Process error...
}

type test struct {
    Str string `json:"str"`
}

testStruct := &test{"string in struct"}

if err := c.Set("foo2", testStruct); err != nil {
    // Process error...
}

if err := c.Set("foo3", []string{"string in slice"}); err != nil {
    // Process error...
}

if err := c.Set("foo4", map[string]string{"bar": "string in map"}); err != nil {
    // Process error...
}

dumpFile := "dump.json"

f, err := os.Create(dumpFile)
if err != nil {
    // Process error...
}

if err := c.Save(f); err != nil {
    f.Close()
    // Process error...
}

f.Close()
c.RemoveAll()

f, err = os.Open(dumpFile)
if err != nil {
    // Process error...
}
defer f.Close()

if err := c.Load(f); err != nil {
    // Process error...
}

str, err := c.Get("foo")
if err != nil {
    // Process error...
}

fmt.Println(str) // Prints: "simple string"

if str, err := c.Get("foo2"); err != nil {
    // Process error...
} else {
    jsonData, err := json.Marshal(str)
    if err != nil {
        // Process error...
    }

    var structData test
    if err := json.Unmarshal(jsonData, &structData); err != nil {
        // Process error...
    }

    // structData.Str == "string in struct".
}

if str, err := c.Get("foo3"); err != nil {
    // Process error...
} else {
    sl := make([]string, len(str.([]any)))
    for i, el := range str.([]any) {
        sl[i] = el.(string)
    }

    // sl[0] == "string in slice".
}

if str, err := c.Get("foo4"); err != nil {
    // Process error...
} else {
    m := make(map[string]string, len(str.(map[string]any)))
    for k, v := range str.(map[string]any) {
        m[k] = v.(string)
    }

    // m["bar"] == "string in map".
}
```

### Some productivity tests:

```bash
BenchmarkCacheGetDataWithLifetime-10            30645175                37.39 ns/op            0 B/op          0 allocs/op
BenchmarkCacheGetData-10                        66297579                18.03 ns/op            0 B/op          0 allocs/op
BenchmarkCacheGetExpiredConcurrent-10            8271970               135.1 ns/op             0 B/op          0 allocs/op
BenchmarkCacheGetDataConcurrent-10               9178712               135.5 ns/op             0 B/op          0 allocs/op
BenchmarkCacheSetWithOpts-10                    21413689                56.22 ns/op            0 B/op          0 allocs/op
BenchmarkCacheSetData-10                        35918688                30.92 ns/op            0 B/op          0 allocs/op
BenchmarkCacheIncrement-10                      25929056                45.62 ns/op            7 B/op          0 allocs/op
BenchmarkCacheDecrement-10                      25951790                45.72 ns/op            7 B/op          0 allocs/op
```
