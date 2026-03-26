package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var ErrTest = errors.New("test error")

// workers is the number of goroutines used in concurrent tests.
var workers = runtime.NumCPU() * 4

func TestEmptyCacheStats(t *testing.T) {
	c := New[string, any]()
	stats := c.Stats()

	if stats.DefaultLifetime != 0 {
		t.Errorf("default lifetime: got: %v; expected: 0",
			stats.DefaultLifetime)
	}

	if stats.CleanupInterval != time.Duration(-1) {
		t.Errorf("default clean-up interval: got %v; expected: 0",
			stats.CleanupInterval)
	}

	if stats.CurrentLength != 0 {
		t.Errorf("size: got %d; expected: 0", stats.CurrentLength)
	}

	if stats.MaxSize != 0 {
		t.Errorf("max size: got %d; expected: 0", stats.MaxSize)
	}

	if stats.MaxLength != 0 {
		t.Errorf("max units: got %d; expected: 0", stats.MaxLength)
	}
}

func TestCacheWithOpts(t *testing.T) {
	c := New(
		WithMaxSize[string, any](52),
		WithMaxUnits[string, any](2),
		WithOnEvictionFn(func(s string, a any) {}),
		// Returns 26 bytes per entry so two entries (2×26=52) fit exactly.
		WithSizeFn(func(_ string, _ any) uint64 { return 26 }),
	)

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo2", "bar2", WithSize[any](100)); !errors.Is(err, ErrMaxSize) {
		t.Errorf("got: %q; expected: %q", err, ErrMaxSize)
	}

	if err := c.Set("faa", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo2", "bar2"); !errors.Is(err, ErrMaxLength) {
		t.Errorf("got: %q; expected: %q", err, ErrMaxLength)
	}

	if c.Size() != 52 {
		t.Errorf("cache size: got %d; expected: 2", c.Size())
	}

	if err := c.ChangeMaxLength(0); err != nil {
		t.Errorf("ChangeMaxLength(0) should disable limit: %v", err)
	}

	c.ChangeMaxLength(3)

	if err := c.Set("foo2", "bar2"); err == nil {
		t.Error(ErrMaxSize)
	} else if !errors.Is(err, ErrMaxSize) {
		t.Errorf("got: %q; expected: %q", err, ErrMaxSize)
	}

	if err := c.ChangeMaxSize(0); err != nil {
		t.Errorf("ChangeMaxSize(0) should disable limit: %v", err)
	}

	c.ChangeMaxSize(100)

	if err := c.Set("foo2", "bar2", WithLifetime[any](uint64(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}

	if c.Length() != 3 {
		t.Errorf("wrong cache size: got: %d; expected: 3", c.Length())
	}

	time.Sleep(500 * time.Millisecond)

	data, err := c.Get("foo2")
	if !errors.Is(err, ErrExpired) {
		t.Error("unit did not expire")
	}

	if data != "bar2" {
		t.Errorf("unit data got: %q; expected: \"bar2\"", data)
	}

	c.ChangeDisplacementPolicy(true)

	if err := c.Set("foo3", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.ChangeMaxLength(5); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo4", "bar"); err != nil {
		t.Fatal(err)
	}

	c.DeleteAll()

	// Changing the size function to report 0 (unknown size) means entries
	// are no longer charged against the size budget.
	c.ChangeSizeFunc(func(_ string, _ any) uint64 { return 0 })

	if err := c.Set("foo", "bar"); err != nil {
		t.Errorf("set with unknown size got: %v", err)
	}
}

func TestCacheScan(t *testing.T) {
	c := New[string, any]()
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo2", "bar2"); err != nil {
		t.Fatal(err)
	}

	res := c.ScanFunc(func(k string, v any) bool {
		return len(k) >= 2 && k[:2] == "fo"
	})

	v1, ok := res["foo"]
	if !ok || v1 != "bar" {
		t.Errorf("key foo: got %q; expected \"bar\"", v1)
	}

	v2, ok := res["foo2"]
	if !ok || v2 != "bar2" {
		t.Errorf("key foo2: got %q; expected \"bar2\"", v2)
	}
}

func TestCacheAdd(t *testing.T) {
	c := New[string, any]()
	if err := c.Add("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Add("foo", "bar"); !errors.Is(err, ErrExist) {
		t.Errorf("add got: %q; expected: %q", err, ErrExist)
	}
}

func TestCacheReplace(t *testing.T) {
	c := New[string, any]()
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Replace("foo", "bar2"); err != nil {
		t.Fatal(err)
	}

	data, err := c.Get("foo")
	if err != nil {
		t.Fatal(err)
	}

	if data != "bar2" {
		t.Errorf("unit data got: %q, expected \"bar2\"", data)
	}

	if err := c.Replace("foo2", ""); !errors.Is(err, ErrNotExist) {
		t.Error(&entryError[string]{"foo2", ErrExist})
	}

	c.ChangeDefaultLifeTime(uint64(100 * time.Millisecond))

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	if err := c.Replace("foo", "bar2"); !errors.Is(err, ErrExpired) {
		t.Errorf("cache rename got: %q: expected: %q", err, ErrExpired)
	}
}

func TestCacheRename(t *testing.T) {
	c := New[string, any]()
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Rename("foo", "foo2"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Get("foo"); !errors.Is(err, ErrNotExist) {
		t.Error(&entryError[string]{"foo", ErrExist})
	}

	data, err := c.Get("foo2")
	if err != nil {
		t.Fatal(err)
	}

	if data != "bar" {
		t.Errorf("unit data got: %q, expected \"bar\"", data)
	}

	// "foo" no longer exists (renamed to "foo2" above); "foo2" does exist.
	// Rename checks the destination key first, so ErrExist is returned
	// before the absent source key is even inspected.
	if err := c.Rename("foo", "foo2"); !errors.Is(err, ErrExist) {
		t.Error(&entryError[string]{"foo2", ErrExist})
	}

	// When neither key exists the source check fires and returns ErrNotExist.
	if err := c.Rename("foo", "foo3"); !errors.Is(err, ErrNotExist) {
		t.Error(&entryError[string]{"foo", ErrNotExist})
	}

	c.ChangeDefaultLifeTime(uint64(100 * time.Millisecond))

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	// "foo" is expired, "foo2" exists → destination checked first → ErrExist.
	if err := c.Rename("foo", "foo2"); !errors.Is(err, ErrExist) {
		t.Errorf("cache rename got: %q: expected: %q", err, ErrExist)
	}

	// "foo" is expired, "foo3" does not exist → destination ok, source expired
	// → entry is revived with the default TTL and stored under "foo3".
	if err := c.Rename("foo", "foo3"); err != nil {
		t.Errorf("cache rename of expired key got: %q: expected success", err)
	}
}

func TestCacheDelete(t *testing.T) {
	done := make(chan struct{}, 1)
	var sb strings.Builder

	goEviction := func() func(string, string) {
		return func(s string, a string) {
			go func() {
				fmt.Fprintf(&sb, "unit %q: remove data: %q", s, a)
				close(done)
			}()
		}
	}

	c := New(
		WithMaxSize[string, string](50),
		WithOnEvictionFn(goEviction()),
	)
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if c.Size() == 0 {
		t.Error("cache size did not change")
	}

	c.Delete("foo")
	c.Delete("foo")

	if c.Size() != 0 {
		t.Errorf("cache size got: %d; expected: 0", c.Size())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			t.Error("delete timed out")
			return
		case <-done:
			if _, err := c.Get("foo"); errors.Is(err, ErrNotExist) {
				if sb.String() != `unit "foo": remove data: "bar"` {
					t.Error("on eviction got:", sb.String())
				}

				return
			} else {
				t.Error(&entryError[string]{"foo", ErrExist})
			}
		}
	}
}

func TestCacheExpired(t *testing.T) {
	c := New(WithDefaultLifetime[string, string](uint64(100 * time.Millisecond)))
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	u, ok := c.entries["foo"]
	if !ok {
		t.Fatal(&entryError[string]{"foo", ErrNotExist})
	}

	if !u.expired() {
		t.Fatal("unit did not expire")
	}
}

func TestCacheRemove(t *testing.T) {
	c := New(WithMaxSize[string, string](1028))
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	c.Remove("foo")
	c.Remove("foo")

	if c.Size() != 0 {
		t.Errorf("cache size got: %d; expected 0", c.Size())
	}
}

func TestCacheRemoveAll(t *testing.T) {
	c := New[string, string]()
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	c.RemoveAll()

	if _, err := c.Get("foo"); !errors.Is(err, ErrNotExist) {
		t.Fatal(&entryError[string]{"foo", ErrExist})
	}

	if c.Size() != 0 {
		t.Errorf("cache size got: %d; expected 0", c.Size())
	}
}

func TestCacheRemoveExpired(t *testing.T) {
	c := New(
		WithDefaultLifetime[string, string](uint64(100*time.Millisecond)),
		WithMaxSize[string, string](1028),
	)
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	c.RemoveExpired()

	if _, err := c.Get("foo"); !errors.Is(err, ErrNotExist) {
		t.Fatal(&entryError[string]{"foo", err})
	}

	c.RemoveExpired()

	if c.Size() != 0 {
		t.Errorf("cache size got: %d; expected 0", c.Size())
	}
}

func TestCacheAlive(t *testing.T) {
	c := New(WithDefaultLifetime[string, string](uint64(100 * time.Millisecond)))
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("bar", "foo", WithLifetime[string](uint64(5*time.Minute))); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	data := c.Alive()

	if _, ok := data["foo"]; ok {
		t.Error("unit did not expire")
	}

	if _, ok := data["bar"]; !ok {
		t.Error("unit expired unexpectedly")
	}
}

func TestCacheRevive(t *testing.T) {
	d := uint64(5 * time.Minute)
	c := New(WithDefaultLifetime[string, string](d))
	if err := c.Set("foo", "bar", WithLifetime[string](uint64(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	if _, err := c.Get("foo"); !errors.Is(err, ErrExpired) {
		t.Error("unit did not expire")
	}

	if err := c.Revive("foo"); err != nil {
		t.Fatal(err)
	}

	u, ok := c.entries["foo"]
	if !ok {
		t.Fatal("unit \"foo\" does not exist")
	}

	if u.expired() {
		t.Fatal("unit expire")
	}

	if err := c.Revive("bar"); !errors.Is(err, ErrNotExist) {
		t.Error(err)
	}
}

func TestCacheReviveUntil(t *testing.T) {
	c := New(WithDefaultLifetime[string, string](uint64(5 * time.Minute)))
	if err := c.Set("foo", "bar", WithLifetime[string](uint64(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	if _, err := c.Get("foo"); !errors.Is(err, ErrExpired) {
		t.Error("unit did not expire")
	}

	d := uint64(2 * time.Minute)

	if err := c.ReviveUntil("foo", d); err != nil {
		t.Fatal(err)
	}

	u, ok := c.entries["foo"]
	if !ok {
		t.Fatal("unit \"foo\" does not exist")
	}

	if u.expired() {
		t.Fatal("unit expire")
	}

	if err := c.ReviveUntil("bar", d); !errors.Is(err, ErrNotExist) {
		t.Error(&entryError[string]{"bar", ErrExist})
	}
}

func TestCacheIncrement(t *testing.T) {
	c := New[string, int]()
	if err := c.Set("test", 0); err != nil {
		t.Fatal(err)
	}

	i, err := Increment(c, "test", 1)
	if err != nil {
		t.Fatal(err)
	}

	if i != 1 {
		t.Errorf("increment failure: got %d", i)
	}

	if n, _ := c.Get("test"); n != i {
		t.Errorf("increment failure: got %d; expected: %d", n, i)
	}

	if err := c.ReviveUntil("test", uint64(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	if _, err := Increment(c, "foo", 1); !errors.Is(err, ErrNotExist) {
		t.Fatal(&entryError[string]{"foo", ErrExist})
	}

	if _, err := Increment(c, "test", 1); !errors.Is(err, ErrExpired) {
		t.Fatal("unit did not expire")
	}
}

func TestCacheDecrement(t *testing.T) {
	c := New[string, int]()
	if err := c.Set("test", 1); err != nil {
		t.Fatal(err)
	}

	i, err := Decrement(c, "test", 1)
	if err != nil {
		t.Fatal(err)
	}

	if i != 0 {
		t.Errorf("decrement failure: got %d", i)
	}

	if n, _ := c.Get("test"); n != i {
		t.Errorf("decrement failure: got %d; expected: %d", n, i)
	}

	if err := c.ReviveUntil("test", uint64(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	if _, err := Decrement(c, "foo", 1); !errors.Is(err, ErrNotExist) {
		t.Fatal(&entryError[string]{"foo", ErrExist})
	}

	if _, err := Decrement(c, "test", 1); !errors.Is(err, ErrExpired) {
		t.Fatal("unit did not expire")
	}
}

func TestCacheJanitor(t *testing.T) {
	c := New(
		WithCleanupInterval[string, string](time.Hour),
		WithDefaultLifetime[string, string](uint64(time.Minute)),
		WithMaxSize[string, string](100),
	)

	var sb strings.Builder

	done := make(chan struct{}, 1)

	c.ChangeOnEvictionFn(func(s string, a string) {
		fmt.Fprintf(&sb, "unit %q: removed", s)
		close(done)
	})

	if err := c.RescheduleCleaning(time.Duration(-1)); !errors.Is(err, ErrDuration) {
		t.Fatal("janitor reschedule did not fail")
	}

	c.StopCleaning()

	if c.j != nil {
		t.Error("janitor was not fired")
	}

	c.ChangeDefaultLifeTime(uint64(100 * time.Millisecond))

	c.OrderCleaning()

	if c.j == nil {
		t.Error("janitor was not hired")
	}

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	u, ok := c.entries["foo"]
	if !ok {
		t.Fatal(&entryError[string]{"foo", ErrNotExist})
	}

	time.Sleep(150 * time.Millisecond)

	if !u.expired() {
		t.Fatal("unit did not expire")
	}

	if err := c.RescheduleCleaning(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			t.Error("clean-up timed out")
			return
		case <-done:
			if _, err := c.Get("foo"); errors.Is(err, ErrNotExist) {
				if sb.String() != `unit "foo": removed` {
					t.Error("on eviction got:", sb.String())
				}

				return
			} else {
				t.Errorf("expected key %q to be cleaned, got: %v", "foo", err)
			}
		}
	}
}

func TestCacheJanitorEviction(t *testing.T) {
	var sb strings.Builder

	done := make(chan struct{}, 1)

	c := New(
		WithCleanupInterval[string, string](250*time.Millisecond),
		WithoutJanitorEviction,
		WithOnEvictionFn(func(s string, a string) {
			fmt.Fprintf(&sb, "unit %q: remove data: %q", s, a)
			close(done)
		}),
	)

	if err := c.Set("foo", "bar", WithLifetime[string](uint64(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	c.StopCleaning()

	if _, err := c.Get("foo"); errors.Is(err, ErrNotExist) {
		if sb.String() != "" {
			t.Error("on eviction got:", sb.String())
		}
	} else {
		t.Errorf("janitor did not remove key %q: %v", "foo", err)
	}

	if err := c.Set("foo", "bar", WithLifetime[string](uint64(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}

	c.ChangeJanitorOnEviction(true)
	c.RescheduleCleaning(250 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			t.Error("clean-up timed out")
			return
		case <-done:
			if _, err := c.Get("foo"); errors.Is(err, ErrNotExist) {
				if sb.String() != `unit "foo": remove data: "bar"` {
					t.Error("on eviction got:", sb.String())
				}

				return
			} else {
				t.Errorf("expected key %q to be cleaned with eviction, got: %v", "foo", err)
			}
		}
	}
}

type readerReturnError struct{}

func (*readerReturnError) Read([]byte) (int, error) { return 0, ErrTest }

type readerNilByte struct{}

func (*readerNilByte) Read(b []byte) (int, error) {
	return 0, io.EOF
}

type writer struct{}

func (*writer) Write([]byte) (int, error) { return 0, nil }

func TestCacheSaveLoad(t *testing.T) {
	c := New(
		WithDefaultLifetime[string, any](uint64(time.Hour)),
		WithMaxSize[string, any](1028),
	)

	if err := c.Set("foo", "simple string"); err != nil {
		t.Fatal(err)
	}

	type test struct {
		Str string `json:"str"`
	}

	testStruct := &test{"string in struct"}

	if err := c.Set("foo2", testStruct); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo3", []string{"string in slice"}); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo4", map[string]string{"bar": "string in map"}); err != nil {
		t.Fatal(err)
	}

	dumpFile := path.Join(t.TempDir(), "dump.json")

	f, err := os.Create(dumpFile)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.Save(f); err != nil {
		f.Close()
		t.Fatal(err)
	}

	f.Close()
	c.RemoveAll()

	f, err = os.Open(dumpFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := c.Load(f); err != nil {
		t.Fatal(err)
	}

	if str, err := c.Get("foo"); err != nil {
		t.Error(err)
	} else if str.(string) != "simple string" {
		t.Errorf("cache load got: %q; expected: %q", str, "simple string")
	}

	if str, err := c.Get("foo2"); err != nil {
		t.Error(err)
	} else {
		jsonData, err := json.Marshal(str)
		if err != nil {
			t.Fatal(err)
		}

		var structData test
		if err := json.Unmarshal(jsonData, &structData); err != nil {
			t.Fatal(err)
		}

		if structData.Str != "string in struct" {
			t.Errorf("cache load got: %v; expected: %v", structData, testStruct)
		}
	}

	if str, err := c.Get("foo3"); err != nil {
		t.Error(err)
	} else {
		sl := make([]string, len(str.([]any)))
		for i, el := range str.([]any) {
			sl[i] = el.(string)
		}

		if sl[0] != "string in slice" {
			t.Errorf("cache load got: %v; expected: %v", sl, []string{"string in slice"})
		}
	}

	if str, err := c.Get("foo4"); err != nil {
		t.Error(err)
	} else {
		m := make(map[string]string, len(str.(map[string]any)))
		for k, v := range str.(map[string]any) {
			m[k] = v.(string)
		}

		v, ok := m["bar"]
		if !ok || v != "string in map" {
			t.Errorf("cache load map value got: %q; expected: %q", v, "string in map")
		}
	}

	if err := c.Load(&readerReturnError{}); !errors.Is(err, ErrTest) {
		t.Errorf("cache load got: %s; expected: %s", err, ErrTest)
	}

	if err := c.Load(&readerNilByte{}); err == nil {
		t.Error("cache load succeeded with nil read buffer")
	}

	c.RemoveAll()

	// Disable size check.
	c.ChangeMaxSize(0)

	if err := c.Set("foo", func() {}); err != nil {
		t.Fatal(err)
	}

	if err := c.Save(&writer{}); err == nil {
		t.Error("cache save succeeded with unsupported type")
	}
}

// TestConcurrentSet verifies that concurrent Set calls on different keys
// do not cause data races.
func TestConcurrentSet(t *testing.T) {
	c := New[int, int]()

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			if err := c.Set(i, i); err != nil {
				t.Errorf("Set key%d: unexpected error: %v", i, err)
			}
		}(i)
	}

	wg.Wait()

	if c.Length() != workers {
		t.Errorf("expected %d keys, got %d", workers, c.Length())
	}
}

// TestConcurrentSetSameKey verifies that concurrent Set calls on the same key
// do not cause data races. The last write wins; final value must be valid.
func TestConcurrentSetSameKey(t *testing.T) {
	c := New[string, int]()

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			if err := c.Set("key", i); err != nil {
				t.Errorf("Set: unexpected error: %v", err)
			}
		}(i)
	}

	wg.Wait()

	v, err := c.Get("key")
	if err != nil {
		t.Errorf("Get after concurrent Set: %v", err)
	}

	if v < 0 || v >= workers {
		t.Errorf("Get: value %d out of expected range [0, %d)", v, workers)
	}
}

// TestConcurrentAdd verifies that concurrent Add calls for the same key
// result in exactly one success. This exercises the atomicity fix in Add.
func TestConcurrentAdd(t *testing.T) {
	c := New[string, int]()

	var wg sync.WaitGroup
	var succeeded atomic.Int64

	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()
			if err := c.Add("key", 1); err == nil {
				succeeded.Add(1)
			}
		}()
	}

	wg.Wait()

	if n := succeeded.Load(); n != 1 {
		t.Errorf("Add: expected exactly 1 success, got %d", n)
	}

	if v, _ := c.Get("key"); v != 1 {
		t.Errorf("Add: expected value 1, got %d", v)
	}
}

// TestConcurrentGetSet runs concurrent reads and writes on overlapping keys.
func TestConcurrentGetSet(t *testing.T) {
	c := New[int, int]()

	for i := range workers {
		c.Set(i, i)
	}

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Get(i % workers)
		}(i)

		go func(i int) {
			defer wg.Done()
			c.Set(i%workers, i*2)
		}(i)
	}

	wg.Wait()
}

// TestConcurrentReplace verifies concurrent Replace calls do not race.
func TestConcurrentReplace(t *testing.T) {
	c := New[string, int]()

	if err := c.Set("key", 0); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			// Replace may return ErrNotExist if a concurrent Delete wins first;
			// that is acceptable behaviour.
			c.Replace("key", i)
		}(i)
	}

	wg.Wait()
}

// TestConcurrentRename verifies concurrent Rename calls on independent keys
// do not cause data races.
func TestConcurrentRename(t *testing.T) {
	c := New[int, int]()

	for i := range workers {
		c.Set(i, i)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Rename(i, -i)
		}(i)
	}

	wg.Wait()

	for i := range workers {
		if _, err := c.Get(-i); err != nil {
			t.Errorf("Rename: expected key %d to exist: %v", -i, err)
		}
	}
}

// TestConcurrentDelete verifies concurrent Delete calls on independent keys
// do not cause data races and drain the cache to empty.
func TestConcurrentDelete(t *testing.T) {
	c := New[int, int]()

	for i := range workers {
		c.Set(i, i)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Delete(i)
		}(i)
	}

	wg.Wait()

	if n := c.Length(); n != 0 {
		t.Errorf("Delete: expected empty cache, got %d keys", n)
	}
}

// TestConcurrentRemove verifies concurrent Remove calls on independent keys
// do not cause data races and correctly account for size.
//
// NOTE: Remove currently updates c.size outside the write lock, so this test
// is expected to expose a data race when run with -race until that is fixed.
func TestConcurrentRemove(t *testing.T) {
	c := New(WithMaxSize[int, int](1 << 20))

	for i := range workers {
		c.Set(i, i)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Remove(i)
		}(i)
	}

	wg.Wait()

	if n := c.Length(); n != 0 {
		t.Errorf("Remove: expected empty cache, got %d keys", n)
	}

	if s := c.Size(); s != 0 {
		t.Errorf("Remove: expected size 0, got %d (possible lost size updates)", s)
	}
}

// TestConcurrentDeleteAll verifies that concurrent DeleteAll and Set calls
// do not cause data races.
func TestConcurrentDeleteAll(t *testing.T) {
	c := New[int, int]()

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Set(i, i)
		}(i)

		go func() {
			defer wg.Done()
			c.DeleteAll()
		}()
	}

	wg.Wait()
}

// TestConcurrentRemoveAll verifies that concurrent RemoveAll and Set calls
// do not cause data races.
func TestConcurrentRemoveAll(t *testing.T) {
	c := New[int, int]()

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Set(i, i)
		}(i)

		go func() {
			defer wg.Done()
			c.RemoveAll()
		}()
	}

	wg.Wait()
}

// TestConcurrentDeleteExpired verifies that concurrent DeleteExpired calls
// do not cause data races.
func TestConcurrentDeleteExpired(t *testing.T) {
	lt := uint64(50 * time.Millisecond)
	c := New(WithDefaultLifetime[int, int](lt))

	for i := range workers {
		c.Set(i, i)
	}

	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()
			c.DeleteExpired()
		}()
	}

	wg.Wait()

	if n := c.Length(); n != 0 {
		t.Errorf("DeleteExpired: expected empty cache, got %d keys", n)
	}
}

// TestConcurrentRemoveExpired verifies that concurrent RemoveExpired calls
// do not cause data races.
func TestConcurrentRemoveExpired(t *testing.T) {
	lt := uint64(50 * time.Millisecond)
	c := New(WithDefaultLifetime[int, int](lt))

	for i := range workers {
		c.Set(i, i)
	}

	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()
			c.RemoveExpired()
		}()
	}

	wg.Wait()

	if n := c.Length(); n != 0 {
		t.Errorf("RemoveExpired: expected empty cache, got %d keys", n)
	}
}

// TestConcurrentIncrement verifies that concurrent Increment calls on the same
// key produce the correct final value.
//
// NOTE: Increment currently reads the value under RLock and writes it back
// under a separate Lock, so concurrent increments can lose updates. This test
// is expected to fail until the read-modify-write is made atomic.
func TestConcurrentIncrement(t *testing.T) {
	c := New[string, int]()

	if err := c.Set("counter", 0); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()
			if _, err := Increment(c, "counter", 1); err != nil {
				t.Errorf("Increment: unexpected error: %v", err)
			}
		}()
	}

	wg.Wait()

	v, err := c.Get("counter")
	if err != nil {
		t.Fatal(err)
	}

	if v != workers {
		t.Errorf("Increment: expected %d, got %d (lost updates)", workers, v)
	}
}

// TestConcurrentDecrement verifies that concurrent Decrement calls on the same
// key produce the correct final value.
//
// NOTE: Same lost-update issue as Increment — expected to fail until fixed.
func TestConcurrentDecrement(t *testing.T) {
	c := New[string, int]()

	if err := c.Set("counter", workers); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()
			if _, err := Decrement(c, "counter", 1); err != nil {
				t.Errorf("Decrement: unexpected error: %v", err)
			}
		}()
	}

	wg.Wait()

	v, err := c.Get("counter")
	if err != nil {
		t.Fatal(err)
	}

	if v != 0 {
		t.Errorf("Decrement: expected 0, got %d (lost updates)", v)
	}
}

// TestConcurrentRevive verifies that concurrent Revive calls on the same key
// do not cause data races.
func TestConcurrentRevive(t *testing.T) {
	lt := uint64(5 * time.Minute)
	c := New(WithDefaultLifetime[string, int](lt))

	if err := c.Set("key", 1); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()
			c.Revive("key")
		}()
	}

	wg.Wait()

	if _, err := c.Get("key"); err != nil {
		t.Errorf("Revive: key missing after concurrent revive: %v", err)
	}
}

// TestConcurrentReviveUntil verifies that concurrent ReviveUntil calls on the
// same key do not cause data races.
func TestConcurrentReviveUntil(t *testing.T) {
	lt := uint64(5 * time.Minute)
	c := New[string, int]()

	if err := c.Set("key", 1); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()
			c.ReviveUntil("key", lt)
		}()
	}

	wg.Wait()

	if _, err := c.Get("key"); err != nil {
		t.Errorf("ReviveUntil: key missing after concurrent revive: %v", err)
	}
}

// TestConcurrentSnapshot verifies that Snapshot is safe to call concurrently
// with Set.
func TestConcurrentSnapshot(t *testing.T) {
	c := New[int, int]()

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Set(i, i)
		}(i)

		go func() {
			defer wg.Done()
			_ = c.Snapshot()
		}()
	}

	wg.Wait()
}

// TestConcurrentAlive verifies that Alive is safe to call concurrently with Set.
func TestConcurrentAlive(t *testing.T) {
	c := New[int, int]()

	var wg sync.WaitGroup
	wg.Add(workers * 2)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Set(i, i)
		}(i)

		go func() {
			defer wg.Done()
			_ = c.Alive()
		}()
	}

	wg.Wait()
}

// TestConcurrentStats verifies that Stats, Length, and Size are safe to call
// concurrently with Set.
func TestConcurrentStats(t *testing.T) {
	c := New[int, int]()

	var wg sync.WaitGroup
	wg.Add(workers * 4)

	for i := range workers {
		go func(i int) {
			defer wg.Done()
			c.Set(i, i)
		}(i)

		go func() { defer wg.Done(); _ = c.Stats() }()
		go func() { defer wg.Done(); _ = c.Length() }()
		go func() { defer wg.Done(); _ = c.Size() }()
	}

	wg.Wait()
}

// populate inserts n entries into c, keyed "key0" … "key{n-1}".
func populate[T any](c *Cache[int, T], n int, val T) {
	for i := range n {
		c.Set(i, val)
	}
}

// BenchmarkSetNew measures Set performance when each call inserts a fresh key,
// growing the cache continuously.
func BenchmarkSetNew(b *testing.B) {
	c := New[int, string]()
	for i := range b.N {
		c.Set(i, "value")
	}
}

// BenchmarkSetNewWithCapacity measures Set with a pre-allocated map.
func BenchmarkSetNewWithCapacity(b *testing.B) {
	c := New(WithCapacity[int, string](b.N))
	for i := range b.N {
		c.Set(i, "value")
	}
}

// BenchmarkSetOverwrite measures Set performance when the same key is
// overwritten on every call (steady-state size of 1).
func BenchmarkSetOverwrite(b *testing.B) {
	c := New[string, string]()
	for range b.N {
		c.Set("key", "value")
	}
}

// BenchmarkSetWithTTL measures Set with a per-entry TTL option.
func BenchmarkSetWithTTL(b *testing.B) {
	c := New[string, string]()
	for range b.N {
		c.Set("key", "value", WithTTL[string](time.Minute))
	}
}

// BenchmarkSetWithSize measures Set when a maxSize limit is configured so
// that each entry triggers size calculation and accounting.
func BenchmarkSetWithSize(b *testing.B) {
	c := New(WithMaxSize[string, string](1 << 30))
	for range b.N {
		c.Set("key", "value", WithSize[string](16))
	}
}

// BenchmarkSetDisplacement measures Set with displacement enabled; the cache
// is kept at capacity so every Set evicts before inserting.
func BenchmarkSetDisplacement(b *testing.B) {
	const cap = 100
	c := New(
		WithMaxEntries[int, string](cap),
		WithDisplacement[int, string],
	)
	populate(c, cap, "value")
	b.ResetTimer()
	for i := range b.N {
		c.Set(i, "value")
	}
}

// BenchmarkSetParallel measures concurrent Set on the same key.
func BenchmarkSetParallel(b *testing.B) {
	c := New[string, string]()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Set("key", "value")
		}
	})
}

// BenchmarkGetHit measures Get when the key is present and live.
func BenchmarkGetHit(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value")
	b.ResetTimer()
	for range b.N {
		c.Get("key")
	}
}

// BenchmarkGetWithTTL measures Get on a live entry that carries an expiry.
func BenchmarkGetWithTTL(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value", WithTTL[string](time.Hour))
	b.ResetTimer()
	for range b.N {
		c.Get("key")
	}
}

// BenchmarkGetMiss measures Get when the key is absent.
func BenchmarkGetMiss(b *testing.B) {
	c := New[string, string]()
	b.ResetTimer()
	for range b.N {
		c.Get("key")
	}
}

// BenchmarkGetExpired measures Get on a key that is present but past its TTL.
func BenchmarkGetExpired(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value", WithTTL[string](time.Nanosecond))
	time.Sleep(time.Millisecond)
	b.ResetTimer()
	for range b.N {
		c.Get("key")
	}
}

// BenchmarkGetParallel measures concurrent read throughput on a single key.
func BenchmarkGetParallel(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Get("key")
		}
	})
}

// BenchmarkGetSetMixedParallel measures throughput when reads and writes
// compete concurrently on the same key (50/50 split).
func BenchmarkGetSetMixedParallel(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				c.Get("key")
			} else {
				c.Set("key", "value")
			}
			i++
		}
	})
}

// BenchmarkAddNew measures Add when the key is absent (succeeds every time).
func BenchmarkAddNew(b *testing.B) {
	c := New[int, string]()
	for i := range b.N {
		c.Add(i, "value")
	}
}

// BenchmarkAddNewWithCapacity measures Add with a pre-allocated map.
func BenchmarkAddNewWithCapacity(b *testing.B) {
	c := New(WithCapacity[int, string](b.N))
	for i := range b.N {
		c.Add(i, "value")
	}
}

// BenchmarkAddConflict measures Add when the key already exists (always
// returns ErrExist — tests the fast-path rejection).
func BenchmarkAddConflict(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value")
	b.ResetTimer()
	for range b.N {
		c.Add("key", "value")
	}
}

// BenchmarkReplace measures Replace on a live key (value changes, TTL and
// size are preserved).
func BenchmarkReplace(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value")
	b.ResetTimer()
	for range b.N {
		c.Replace("key", "new-value")
	}
}

// BenchmarkDelete measures Delete (fires the eviction callback if set).
// The key is re-inserted before each timed operation.
func BenchmarkDelete(b *testing.B) {
	c := New[string, string]()
	for range b.N {
		c.Set("key", "value")
		c.Delete("key")
	}
}

// BenchmarkRemove measures silent Remove (no eviction callback).
func BenchmarkRemove(b *testing.B) {
	c := New[string, string]()
	for range b.N {
		c.Set("key", "value")
		c.Remove("key")
	}
}

// BenchmarkDeleteAll measures DeleteAll on a populated cache.
func BenchmarkDeleteAll(b *testing.B) {
	b.StopTimer()
	caches := make([]*Cache[int, string], b.N)
	for i := range b.N {
		caches[i] = New[int, string]()
		populate(caches[i], 10, "value")
	}
	b.StartTimer()
	for i := range b.N {
		caches[i].DeleteAll()
	}
}

// BenchmarkRemoveAll measures RemoveAll (no eviction callbacks) on a cache.
func BenchmarkRemoveAll(b *testing.B) {
	b.StopTimer()
	caches := make([]*Cache[int, string], b.N)
	for i := range b.N {
		caches[i] = New[int, string]()
		populate(caches[i], 10, "value")
	}
	b.StartTimer()
	for i := range b.N {
		caches[i].RemoveAll()
	}
}

// BenchmarkRename measures the cost of renaming an existing key.
func BenchmarkRename(b *testing.B) {
	c := New[int, string]()
	for i := range b.N {
		c.Set(i, "value")
	}
	b.ResetTimer()
	for i := range b.N {
		c.Rename(i, -(i + 1))
	}
}

// BenchmarkRevive measures Revive using the cache default TTL.
func BenchmarkRevive(b *testing.B) {
	c := New(WithDefaultTTL[string, string](5 * time.Minute))
	c.Set("key", "value")
	b.ResetTimer()
	for range b.N {
		c.Revive("key")
	}
}

// BenchmarkReviveFor measures ReviveFor with an explicit duration.
func BenchmarkReviveFor(b *testing.B) {
	c := New[string, string]()
	c.Set("key", "value", WithTTL[string](5*time.Minute))
	b.ResetTimer()
	for range b.N {
		c.ReviveFor("key", 5*time.Minute)
	}
}

func BenchmarkAlive(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			c := New[int, string]()
			populate(c, n, "value")
			b.ResetTimer()
			for range b.N {
				c.Alive()
			}
		})
	}
}

func BenchmarkSnapshot(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			c := New[int, string]()
			populate(c, n, "value")
			b.ResetTimer()
			for range b.N {
				c.Snapshot()
			}
		})
	}
}

func BenchmarkScanFunc(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			c := New[int, string]()
			populate(c, n, "value")
			b.ResetTimer()
			for range b.N {
				c.ScanFunc(func(k int, v string) bool { return k > 4 })
			}
		})
	}
}

func BenchmarkIncrement(b *testing.B) {
	c := New[string, int]()
	c.Set("counter", 0)
	b.ResetTimer()
	for range b.N {
		Increment(c, "counter", 1)
	}
}

func BenchmarkDecrement(b *testing.B) {
	c := New[string, int]()
	c.Set("counter", 0)
	b.ResetTimer()
	for range b.N {
		Decrement(c, "counter", 1)
	}
}

func BenchmarkIncrementParallel(b *testing.B) {
	c := New[string, int]()
	c.Set("counter", 0)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			Increment(c, "counter", 1)
		}
	})
}

// BenchmarkDeleteExpired measures DeleteExpired on a cache where all
// entries have already expired.
func BenchmarkDeleteExpired(b *testing.B) {
	b.StopTimer()
	caches := make([]*Cache[int, string], b.N)
	for i := range b.N {
		caches[i] = New[int, string]()
		for j := range 10 {
			caches[i].Set(j, "value", WithTTL[string](time.Nanosecond))
		}
	}
	time.Sleep(time.Millisecond)
	b.StartTimer()
	for i := range b.N {
		caches[i].DeleteExpired()
	}
}

// BenchmarkRemoveExpired measures RemoveExpired on a cache where all
// entries have already expired.
func BenchmarkRemoveExpired(b *testing.B) {
	b.StopTimer()
	caches := make([]*Cache[int, string], b.N)
	for i := range b.N {
		caches[i] = New[int, string]()
		for j := range 10 {
			caches[i].Set(j, "value", WithTTL[string](time.Nanosecond))
		}
	}
	time.Sleep(time.Millisecond)
	b.StartTimer()
	for i := range b.N {
		caches[i].RemoveExpired()
	}
}

func BenchmarkLen(b *testing.B) {
	c := New[int, string]()
	populate(c, 10, "value")
	b.ResetTimer()
	for range b.N {
		c.Len()
	}
}

func BenchmarkSize(b *testing.B) {
	c := New(WithMaxSize[int, string](1 << 30))
	populate(c, 10, "value")
	b.ResetTimer()
	for range b.N {
		c.Size()
	}
}

func BenchmarkStats(b *testing.B) {
	c := New[int, string]()
	populate(c, 10, "value")
	b.ResetTimer()
	for range b.N {
		c.Stats()
	}
}
