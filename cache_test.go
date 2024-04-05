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
	"testing"
	"time"
)

var ErrTest = errors.New("test error")

func TestEmptyCacheStats(t *testing.T) {
	c := New()
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
		WithMaxSize(52),
		WithMaxUnits(2),
		WithOnEviction(func(s string, a any) {}),
		WithDataSize(getSizeOf),
	)

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo2", "bar2", WithSize(100)); !errors.Is(err, ErrMaxSize) {
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

	if err := c.ChangeMaxLength(0); !errors.Is(err, ErrMaxLength) {
		t.Error("cache length change did not fail")
	}

	c.ChangeMaxLength(3)

	if err := c.Set("foo2", "bar2"); err == nil {
		t.Error(ErrMaxSize)
	} else if !errors.Is(err, ErrMaxSize) {
		t.Errorf("got: %q; expected: %q", err, ErrMaxSize)
	}

	if err := c.ChangeMaxSize(0); !errors.Is(err, ErrMaxSize) {
		t.Error("cache size change did not fail")
	}

	c.ChangeMaxSize(100)

	if err := c.Set("foo2", "bar2", WithLifetime(uint64(100*time.Millisecond))); err != nil {
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

	c.DeleteAll()

	type testStruct struct{ data string }

	v := &testStruct{data: "test data"}

	if err := c.Set("foo", v); err == nil {
		t.Fatal("unknown struct was serialized")
	}

	c.ChangeSizeFn(func(s string, a any) (uint64, error) {
		return 0, ErrTest
	})

	var dstErr *unitError

	expected := "key \"foo\": test error"

	if err := c.Set("foo", "bar"); !errors.As(err, &dstErr) ||
		err.Error() != expected {
		t.Errorf("got: %q; expected: %q", err, expected)
	}
}

func TestCacheScan(t *testing.T) {
	c := New()
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("foo2", "bar2"); err != nil {
		t.Fatal(err)
	}

	res := c.Scan("fo")

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
	c := New()
	if err := c.Add("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Add("foo", "bar"); !errors.Is(err, ErrExists) {
		t.Errorf("add got: %q; expected: %q", err, ErrExists)
	}
}

func TestCacheReplace(t *testing.T) {
	c := New()
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

	if err := c.Replace("foo2", ""); !errors.Is(err, ErrNotExists) {
		t.Error(&unitError{"foo2", ErrExists})
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
	c := New()
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Rename("foo", "foo2"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Get("foo"); !errors.Is(err, ErrNotExists) {
		t.Error(&unitError{"foo", ErrExists})
	}

	data, err := c.Get("foo2")
	if err != nil {
		t.Fatal(err)
	}

	if data != "bar" {
		t.Errorf("unit data got: %q, expected \"bar\"", data)
	}

	if err := c.Rename("foo", "foo2"); !errors.Is(err, ErrNotExists) {
		t.Error(&unitError{"foo", ErrExists})
	}

	c.ChangeDefaultLifeTime(uint64(100 * time.Millisecond))

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	if err := c.Rename("foo", "foo2"); !errors.Is(err, ErrExpired) {
		t.Errorf("cache rename got: %q: expected: %q", err, ErrExpired)
	}
}

func TestCacheDelete(t *testing.T) {
	done := make(chan struct{}, 1)
	var sb strings.Builder

	c := New(
		WithMaxSize(50),
		WithOnEviction(func(s string, a any) {
			sb.WriteString(fmt.Sprintf("unit %q: removed", s))
			close(done)
		}),
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
			if _, err := c.Get("foo"); errors.Is(err, ErrNotExists) {
				if sb.String() != `unit "foo": removed` {
					t.Error("on eviction got:", sb.String())
				}

				return
			} else {
				t.Error(&unitError{"foo", ErrExists})
			}
		}
	}
}

func TestCacheExpired(t *testing.T) {
	c := New(WithDefaultLifetime(uint64(100 * time.Millisecond)))
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	u, ok := c.getUnit("foo")
	if !ok {
		t.Fatal(&unitError{"foo", ErrNotExists})
	}

	if !u.expired() {
		t.Fatal("unit did not expire")
	}
}

func TestCacheRemove(t *testing.T) {
	c := New(WithMaxSize(1028))
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
	c := New()
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	c.RemoveAll()

	if _, err := c.Get("foo"); !errors.Is(err, ErrNotExists) {
		t.Fatal(&unitError{"foo", ErrExists})
	}

	if c.Size() != 0 {
		t.Errorf("cache size got: %d; expected 0", c.Size())
	}
}

func TestCacheRemoveExpired(t *testing.T) {
	c := New(WithDefaultLifetime(uint64(100*time.Millisecond)), WithMaxSize(1028))
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)

	c.RemoveExpired()

	if _, err := c.Get("foo"); !errors.Is(err, ErrNotExists) {
		t.Fatal(&unitError{"foo", err})
	}

	c.RemoveExpired()

	if c.Size() != 0 {
		t.Errorf("cache size got: %d; expected 0", c.Size())
	}
}

func TestCacheAlive(t *testing.T) {
	c := New(WithDefaultLifetime(uint64(100 * time.Millisecond)))
	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if err := c.Set("bar", "foo", WithLifetime(uint64(5*time.Minute))); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	data := c.Alive()

	if _, ok := data["foo"]; ok {
		t.Error("unit did not expire")
	}

	if _, ok := data["bar"]; !ok {
		t.Error("unit did not expired")
	}
}

func TestCacheRevive(t *testing.T) {
	d := uint64(5 * time.Minute)
	c := New(WithDefaultLifetime(d))
	if err := c.Set("foo", "bar", WithLifetime(uint64(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}

	time.Sleep(500 * time.Millisecond)

	if _, err := c.Get("foo"); !errors.Is(err, ErrExpired) {
		t.Error("unit did not expire")
	}

	if err := c.Revive("foo"); err != nil {
		t.Fatal(err)
	}

	u, ok := c.getUnit("foo")
	if !ok {
		t.Fatal("unit \"foo\" does not exist")
	}

	if u.expired() {
		t.Fatal("unit did not expire")
	}

	if err := c.Revive("bar"); !errors.Is(err, ErrNotExists) {
		t.Error(err)
	}
}

func TestCacheReviveUntil(t *testing.T) {
	c := New(WithDefaultLifetime(uint64(5 * time.Minute)))
	if err := c.Set("foo", "bar", WithLifetime(uint64(100*time.Millisecond))); err != nil {
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

	u, ok := c.getUnit("foo")
	if !ok {
		t.Fatal("unit \"foo\" does not exist")
	}

	if u.expired() {
		t.Fatal("unit did not expire")
	}

	if err := c.ReviveUntil("bar", d); !errors.Is(err, ErrNotExists) {
		t.Error(&unitError{"bar", ErrExists})
	}
}

func TestCacheIncrement(t *testing.T) {
	c := New()
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

	if _, err := Increment(c, "foo", 1); !errors.Is(err, ErrNotExists) {
		t.Fatal(&unitError{"foo", ErrExists})
	}

	if _, err := Increment(c, "test", 1); !errors.Is(err, ErrExpired) {
		t.Fatal("unit did not expire")
	}

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if _, err := Increment(c, "foo", 1); !errors.Is(err, ErrNotInt) {
		t.Fatal("string was incremented")
	}
}

func TestCacheDecrement(t *testing.T) {
	c := New()
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

	if _, err := Decrement(c, "foo", 1); !errors.Is(err, ErrNotExists) {
		t.Fatal(&unitError{"foo", ErrExists})
	}

	if _, err := Decrement(c, "test", 1); !errors.Is(err, ErrExpired) {
		t.Fatal("unit did not expire")
	}

	if err := c.Set("foo", "bar"); err != nil {
		t.Fatal(err)
	}

	if _, err := Decrement(c, "foo", 1); !errors.Is(err, ErrNotInt) {
		t.Fatal("string was incremented")
	}
}

func TestCacheJanitor(t *testing.T) {
	c := New(
		WithCleanupInteval(time.Hour),
		WithDefaultLifetime(uint64(time.Minute)),
		WithMaxSize(100),
	)

	var sb strings.Builder

	done := make(chan struct{}, 1)

	c.ChangeOnEviction(func(s string, a any) {
		sb.WriteString(fmt.Sprintf("unit %q: removed", s))
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

	u, ok := c.getUnit("foo")
	if !ok {
		t.Fatal(&unitError{"foo", ErrNotExists})
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
			if _, err := c.Get("foo"); errors.Is(err, ErrNotExists) {
				if sb.String() != `unit "foo": removed` {
					t.Error("on eviction got:", sb.String())
				}

				return
			} else {
				t.Error(&unitError{"foo", ErrExists})
			}
		}
	}
}

func TestCacheJanitorEviction(t *testing.T) {
	var sb strings.Builder

	done := make(chan struct{}, 1)

	c := New(
		WithCleanupInteval(250*time.Millisecond),
		WithoutJanitorEviction,
		WithOnEviction(func(s string, a any) {
			sb.WriteString(fmt.Sprintf("unit %q: removed", s))
			close(done)
		}),
	)

	if err := c.Set("foo", "bar", WithLifetime(uint64(100*time.Millisecond))); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	c.StopCleaning()

	if _, err := c.Get("foo"); errors.Is(err, ErrNotExists) {
		if sb.String() != "" {
			t.Error("on eviction got:", sb.String())
		}
	} else {
		t.Error(&unitError{"foo", ErrExists})
	}

	if err := c.Set("foo", "bar", WithLifetime(uint64(100*time.Millisecond))); err != nil {
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
			if _, err := c.Get("foo"); errors.Is(err, ErrNotExists) {
				if sb.String() != `unit "foo": removed` {
					t.Error("on eviction got:", sb.String())
				}

				return
			} else {
				t.Error(&unitError{"foo", ErrExists})
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
		WithDefaultLifetime(uint64(time.Hour)),
		WithMaxSize(1028),
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
		t.Error("cache load successed with nil read buffer")
	}

	c.RemoveAll()

	// Disable size check.
	c.ChangeMaxSize(0)

	if err := c.Set("foo", func() {}); err != nil {
		t.Fatal(err)
	}

	if err := c.Save(&writer{}); err == nil {
		t.Error("cache save successed with unsupported type")
	}
}

func BenchmarkCacheGetDataWithLifetime(b *testing.B) {
	b.StopTimer()
	c := New()
	c.Set("foo", "bar", WithLifetime(uint64(5*time.Minute)))
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		c.Get("foo")
	}
}

func BenchmarkCacheGetData(b *testing.B) {
	b.StopTimer()
	c := New()
	c.Set("foo", "bar")
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		c.Get("foo")
	}
}

func BenchmarkCacheGetExpiredConcurrent(b *testing.B) {
	b.StopTimer()
	c := New()
	c.Set("foo", "bar", WithLifetime(uint64(5*time.Minute)))
	wg := new(sync.WaitGroup)
	workers := runtime.NumCPU()
	each := b.N / workers
	wg.Add(workers)
	b.StartTimer()
	for i := 0; i < workers; i++ {
		go func() {
			for j := 0; j < each; j++ {
				c.Get("foo")
			}
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCacheGetDataConcurrent(b *testing.B) {
	b.StopTimer()
	c := New()
	c.Set("foo", "bar")
	wg := new(sync.WaitGroup)
	workers := runtime.NumCPU()
	each := b.N / workers
	wg.Add(workers)
	b.StartTimer()
	for i := 0; i < workers; i++ {
		go func() {
			for j := 0; j < each; j++ {
				c.Get("foo")
			}
			wg.Done()
		}()
	}
	wg.Wait()
}

func BenchmarkCacheSetWithOpts(b *testing.B) {
	b.StopTimer()
	c := New()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		c.Set(
			"foo",
			"bar",
			WithLifetime(uint64(100*time.Millisecond)),
			WithSize(20),
		)
	}
}

func BenchmarkCacheSetData(b *testing.B) {
	b.StopTimer()
	c := New()
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		c.Set("foo", "bar")
	}
}

func BenchmarkCacheIncrement(b *testing.B) {
	b.StopTimer()
	c := New()
	if err := c.Set("test", -1); err != nil {
		b.Error(err)
	}
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		Increment(c, "test", 1)
	}
}

func BenchmarkCacheDecrement(b *testing.B) {
	b.StopTimer()
	c := New()
	if err := c.Set("test", b.N+1); err != nil {
		b.Error(err)
	}
	b.StartTimer()
	for i := b.N; i > 0; i-- {
		Decrement(c, "test", 1)
	}
}

func ExampleCache() {
	c := New()

	if err := c.Set("foo", "bar"); err != nil {
		// Process error...
	}

	data, err := c.Get("foo")
	if err != nil {
		// Process error...
	}

	fmt.Println(data) // Prints: "bar".
}

func ExampleCache_withOptions() {
	c := New(
		WithCleanupInteval(time.Minute),
		WithDefaultLifetime(uint64(500*time.Millisecond)),
	)

	if err := c.Set("foo", "bar"); err != nil {
		// Process error...
	}

	time.Sleep(1 * time.Second)

	data, err := c.Get("foo")
	fmt.Println(data) // Prints: "bar".
	fmt.Println(err)  // Prints: key "foo": is expired.

	c.RemoveExpired()

	_, err = c.Get("foo")
	fmt.Println(err) // Prints: key "foo": does not exist.

	if err := c.Set("foo", "bar", WithLifetime(uint64(1*time.Second))); err != nil {
		// Process error...
	}

	time.Sleep(500 * time.Millisecond)

	m := c.Alive()
	if v, ok := m["foo"]; ok {
		fmt.Println(v) // Prints: "bar".
	}
}

func ExampleCache_saveLoad() {
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

	fmt.Println(str) // Prints: "simple string".

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
}
