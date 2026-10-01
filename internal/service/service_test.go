package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeghdadFadaee/ask-53/internal/ai"
	"github.com/MeghdadFadaee/ask-53/internal/config"
)

type providerFunc func(context.Context, string) (string, error)

func (f providerFunc) Answer(ctx context.Context, s string) (string, error) { return f(ctx, s) }

var local = netip.MustParseAddr("127.0.0.1")

func setup(t testing.TB, change func(*config.Config), p Provider) (*Engine, *Stats) {
	t.Helper()
	c := config.Defaults()
	c.AIRate = config.Rate{PerSecond: 1000, Burst: 1000}
	c.ClientAIRate = config.Rate{PerSecond: 1000, Burst: 1000}
	c.BudgetFile = filepath.Join(t.TempDir(), "budget.json")
	if change != nil {
		change(&c)
	}
	b, e := OpenBudget(c.BudgetFile, c.DailyCalls)
	if e != nil {
		t.Fatal(e)
	}
	s := new(Stats)
	g := NewGate(c)
	engine := NewEngine(c, p, b, g, s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { engine.Close(); b.Close() })
	return engine, s
}
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestCoalescingAndCache(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	e, s := setup(t, nil, providerFunc(func(ctx context.Context, q string) (string, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return "Tehran", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}))
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			r, err := e.Get(context.Background(), "capital of iran", local)
			if err != nil || r.Text != "Tehran" {
				t.Errorf("%v %v", r, err)
			}
		}()
	}
	<-started
	waitFor(t, func() bool { return s.Coalesced.Load() == n-1 })
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate call")
	}
	r, err := e.Get(context.Background(), "capital of iran", local)
	if err != nil || r.Text != "Tehran" || s.CacheHits.Load() != 1 {
		t.Fatal("cache miss", err)
	}
}
func TestCancelledWaiterDoesNotCancelSharedCall(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	e, _ := setup(t, nil, providerFunc(func(ctx context.Context, q string) (string, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { _, err := e.Get(ctx, "q", local); done <- err }()
	<-started
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("not cancelled")
	}
	close(release)
	if _, err := e.Get(context.Background(), "q", local); err != nil || calls.Load() != 1 {
		t.Fatal("shared call lost", err)
	}
}
func TestCapacityAndShutdown(t *testing.T) {
	started := make(chan struct{})
	e, _ := setup(t, func(c *config.Config) { c.MaxInflight = 1 }, providerFunc(func(ctx context.Context, q string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}))
	done := make(chan error)
	go func() { _, err := e.Get(context.Background(), "one", local); done <- err }()
	<-started
	if _, err := e.Get(context.Background(), "two", local); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	e.Close()
	if <-done == nil {
		t.Fatal("call not canceled")
	}
	if _, err := e.Get(context.Background(), "one", local); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
func TestLRUExpiryAndFailureCache(t *testing.T) {
	var calls atomic.Int64
	e, _ := setup(t, func(c *config.Config) { c.CacheEntries = 2; c.CircuitFailures = 100 }, providerFunc(func(ctx context.Context, q string) (string, error) {
		calls.Add(1)
		if q == "bad" {
			return "", &ai.Error{Kind: "http_503"}
		}
		return q, nil
	}))
	get := func(q string) { e.Get(context.Background(), q, local) }
	get("a")
	get("b")
	get("a")
	get("c")
	get("b")
	if calls.Load() != 4 {
		t.Fatal("LRU eviction incorrect", calls.Load())
	}
	get("bad")
	get("bad")
	if calls.Load() != 5 {
		t.Fatal("failure cache missing")
	}
	e.mu.Lock()
	el := e.cache["bad"]
	v := el.Value.(entry)
	v.result.Expires = time.Now().Add(-time.Second)
	el.Value = v
	e.mu.Unlock()
	get("bad")
	if calls.Load() != 6 {
		t.Fatal("expired error not retried")
	}
}
func TestCircuitHalfOpen(t *testing.T) {
	var calls atomic.Int64
	probeStarted := make(chan struct{})
	release := make(chan struct{})
	e, _ := setup(t, func(c *config.Config) { c.CircuitFailures = 2 }, providerFunc(func(ctx context.Context, q string) (string, error) {
		n := calls.Add(1)
		if n < 3 {
			return "", &ai.Error{Kind: "http_503"}
		}
		close(probeStarted)
		select {
		case <-release:
			return "recovered", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}))
	e.Get(context.Background(), "a", local)
	e.Get(context.Background(), "b", local)
	if _, err := e.Get(context.Background(), "c", local); !errors.Is(err, ErrCircuit) {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.openUntil = time.Now().Add(-time.Second)
	e.mu.Unlock()
	done := make(chan error)
	go func() { _, err := e.Get(context.Background(), "c", local); done <- err }()
	<-probeStarted
	if _, err := e.Get(context.Background(), "d", local); !errors.Is(err, ErrCircuit) {
		t.Fatal("second probe admitted", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e.Snapshot()["circuit_open"] != false {
		t.Fatal("circuit did not recover")
	}
}
func TestRetryAfterOpensCircuitImmediately(t *testing.T) {
	e, _ := setup(t, nil, providerFunc(func(context.Context, string) (string, error) {
		return "", &ai.Error{Kind: "http_429", RetryAfter: time.Minute}
	}))
	e.Get(context.Background(), "a", local)
	if _, err := e.Get(context.Background(), "b", local); !errors.Is(err, ErrCircuit) {
		t.Fatal(err)
	}
}
func TestRateAndBudgetPreventProviderWork(t *testing.T) {
	for _, kind := range []string{"rate", "budget"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int64
			e, _ := setup(t, func(c *config.Config) {
				if kind == "rate" {
					c.AIRate = config.Rate{PerSecond: 0.0001, Burst: 1}
				} else {
					c.DailyCalls = 1
				}
			}, providerFunc(func(context.Context, string) (string, error) { calls.Add(1); return "ok", nil }))
			e.Get(context.Background(), "a", local)
			_, err := e.Get(context.Background(), "b", local)
			want := ErrRate
			if kind == "budget" {
				want = ErrBudget
			}
			if !errors.Is(err, want) || calls.Load() != 1 {
				t.Fatal("unexpected work", calls.Load(), err)
			}
			if _, err = e.Get(context.Background(), "a", local); err != nil {
				t.Fatal("cached answer blocked", err)
			}
		})
	}
}
func TestBudgetRestartLockAndUTC(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	b, err := OpenBudget(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenBudget(path, 2); err == nil {
		t.Fatal("second process accepted")
	}
	now := time.Now()
	if err = b.Reserve(now); err != nil {
		t.Fatal(err)
	}
	b.Close()
	b, err = OpenBudget(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = b.Reserve(now); err != nil {
		t.Fatal(err)
	}
	if err = b.Reserve(now); !errors.Is(err, ErrBudget) {
		t.Fatal("budget reset on restart", err)
	}
	if err = b.Reserve(now.Add(-24 * time.Hour)); !errors.Is(err, ErrBudget) {
		t.Fatal("rollback bypass", err)
	}
	if err = b.Reserve(now.Add(24 * time.Hour)); err != nil {
		t.Fatal("no rollover", err)
	}
}
func TestBudgetCorruptionAndPersistenceFailure(t *testing.T) {
	for _, data := range []string{`garbage`, `{"day":"2026-01-01","calls":-1}`, `{"day":"wrong","calls":0}`, `{"day":"2026-01-01","calls":0} {}`, `null`, `{"day":"2026-01-01"}`, `{}`} {
		path := filepath.Join(t.TempDir(), "budget.json")
		os.WriteFile(path, []byte(data), 0600)
		if b, err := OpenBudget(path, 2); err == nil {
			b.Close()
			t.Fatalf("accepted %s", data)
		}
	}
	dir := filepath.Join(t.TempDir(), "state")
	b, err := OpenBudget(filepath.Join(dir, "budget.json"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	os.RemoveAll(dir)
	if err = b.Reserve(time.Now()); !errors.Is(err, ErrBudget) {
		t.Fatal(err)
	}
	os.MkdirAll(dir, 0700)
	if err = b.Reserve(time.Now()); !errors.Is(err, ErrBudget) {
		t.Fatal("disk failure was not latched")
	}
}
func TestGateBoundsAndIPv6Prefix(t *testing.T) {
	c := config.Defaults()
	c.AllowedCIDRs = []string{"0.0.0.0/0", "::/0"}
	c.ClientEntries = 1
	c.ClientRate = config.Rate{PerSecond: 1, Burst: 1}
	c.ClientAIRate = config.Rate{PerSecond: 1, Burst: 1}
	g := NewGate(c)
	now := time.Now()
	a := netip.MustParseAddr("2001:db8::1")
	b := netip.MustParseAddr("2001:db8::2")
	if !g.take(a, false, now) || g.take(b, false, now) {
		t.Fatal("IPv6 rotation bypassed rate")
	}
	if g.take(local, false, now) {
		t.Fatal("evicted active bucket")
	}
	if !g.take(local, false, now.Add(c.ClientIdleTTL.Value()+time.Second)) {
		t.Fatal("idle table not pruned")
	}
	if prefix(netip.MustParseAddr("::ffff:127.0.0.1")) != prefix(local) {
		t.Fatal("mapped IP bypass")
	}
}
func TestConcurrentBudget(t *testing.T) {
	b, err := OpenBudget(filepath.Join(t.TempDir(), "budget.json"), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var successes atomic.Int64
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Reserve(time.Now()) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 7 {
		t.Fatal("overspent", successes.Load())
	}
}

func TestConcurrentSuccessCannotCancelRetryAfter(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	e, _ := setup(t, nil, providerFunc(func(ctx context.Context, q string) (string, error) {
		if q == "slow" {
			close(started)
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "", &ai.Error{Kind: "http_429", RetryAfter: time.Minute}
	}))
	done := make(chan error)
	go func() { _, err := e.Get(context.Background(), "slow", local); done <- err }()
	<-started
	e.Get(context.Background(), "rate", local)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get(context.Background(), "third", local); !errors.Is(err, ErrCircuit) {
		t.Fatal("concurrent success canceled Retry-After", err)
	}
}
func TestAlreadyCancelledDoesNotSpend(t *testing.T) {
	e, s := setup(t, nil, providerFunc(func(context.Context, string) (string, error) { t.Error("provider called"); return "", nil }))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Get(ctx, "q", local); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.Calls.Load() != 0 {
		t.Fatal("paid work started")
	}
	_, n, _ := e.budget.Snapshot()
	if n != 0 {
		t.Fatal("budget consumed")
	}
}
func BenchmarkCachedAnswer(b *testing.B) {
	e, _ := setup(b, nil, providerFunc(func(context.Context, string) (string, error) { return "Tehran", nil }))
	if _, err := e.Get(context.Background(), "capital of iran", local); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := e.Get(context.Background(), "capital of iran", local); err != nil {
			b.Fatal(err)
		}
	}
}
