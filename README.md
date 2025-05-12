## Simple cache package

This package is a simple in-memory key-value storage based on golang map type. Check [docs](https://pkg.go.dev/github.com/emar-kar/cache) for more details.

### Import

```go
import "github.com/emar-kar/cache"
```

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

### Eviction with goroutines

Since it was decided to remove explicit goroutine call for eviction functions in `Delete*` methods, here is the workaround how to implement this anyway:

```go
goEviction := func() func(string, any) {
    return func(str string, a any) {
        go func() {
            // Do something with key and value...
        }()
    }
}

c := New(WithOnEvictionFn(goEviction()))

if err := c.Set("foo", "simple string"); err != nil {
    // Process error...
}

c.Delete("foo")

// Wait until goroutine finish onEviction...
```

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
BenchmarkCacheGetDataWithLifetime-10            34216148                35.00 ns/op            0 B/op          0 allocs/op
BenchmarkCacheGetData-10                        83719711                13.92 ns/op            0 B/op          0 allocs/op
BenchmarkCacheGetExpiredConcurrent-10            7922000                143.2 ns/op            0 B/op          0 allocs/op
BenchmarkCacheGetDataConcurrent-10              11062910                140.3 ns/op            0 B/op          0 allocs/op
BenchmarkCacheSetWithOpts-10                    20568798                59.04 ns/op            0 B/op          0 allocs/op
BenchmarkCacheSetData-10                        28863961                41.16 ns/op            0 B/op          0 allocs/op
BenchmarkCacheIncrement-10                      33345606                36.13 ns/op            0 B/op          0 allocs/op
BenchmarkCacheDecrement-10                      32169246                36.19 ns/op            0 B/op          0 allocs/op
```
