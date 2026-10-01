package service

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeghdadFadaee/ask-53/internal/ai"
	"github.com/MeghdadFadaee/ask-53/internal/config"
)

var (
	ErrBusy    = errors.New("AI work capacity reached")
	ErrRate    = errors.New("AI rate limit reached")
	ErrCircuit = errors.New("AI circuit open")
	ErrClosed  = errors.New("service shutting down")
)

type Provider interface {
	Answer(context.Context, string) (string, error)
}
type Stats struct{ Requests, Denied, Malformed, Limited, Truncated, Answers, WriteErrors, CacheHits, Coalesced, Calls, Failures, BudgetDenied, CircuitDenied atomic.Uint64 }

func (s *Stats) Snapshot() map[string]uint64 {
	return map[string]uint64{"requests": s.Requests.Load(), "denied": s.Denied.Load(), "malformed": s.Malformed.Load(), "limited": s.Limited.Load(), "truncated": s.Truncated.Load(), "answers": s.Answers.Load(), "write_errors": s.WriteErrors.Load(), "cache_hits": s.CacheHits.Load(), "coalesced": s.Coalesced.Load(), "ai_calls": s.Calls.Load(), "ai_failures": s.Failures.Load(), "budget_denied": s.BudgetDenied.Load(), "circuit_denied": s.CircuitDenied.Load()}
}

type Result struct {
	Text    string
	Expires time.Time
	Err     error
}
type entry struct {
	key    string
	result Result
}
type flight struct {
	probe  bool
	done   chan struct{}
	result Result
}
type Engine struct {
	mu        sync.Mutex
	cfg       config.Config
	provider  Provider
	budget    *Budget
	gate      *Gate
	stats     *Stats
	log       *slog.Logger
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	closed    bool
	cache     map[string]*list.Element
	lru       *list.List
	flights   map[string]*flight
	rate      bucket
	failures  int
	openUntil time.Time
	probe     bool
}

func NewEngine(c config.Config, p Provider, b *Budget, g *Gate, s *Stats, log *slog.Logger) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{cfg: c, provider: p, budget: b, gate: g, stats: s, log: log, ctx: ctx, cancel: cancel, cache: make(map[string]*list.Element), lru: list.New(), flights: make(map[string]*flight), rate: newBucket(c.AIRate, time.Now())}
}
func (e *Engine) Get(ctx context.Context, key string, client netip.Addr) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	now := time.Now()
	e.mu.Lock()
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		return Result{}, err
	}
	if e.closed {
		e.mu.Unlock()
		return Result{}, ErrClosed
	}
	if el := e.cache[key]; el != nil {
		v := el.Value.(entry)
		if now.Before(v.result.Expires) {
			e.lru.MoveToFront(el)
			e.stats.CacheHits.Add(1)
			e.mu.Unlock()
			return v.result, v.result.Err
		}
		e.lru.Remove(el)
		delete(e.cache, key)
	}
	if f := e.flights[key]; f != nil {
		e.stats.Coalesced.Add(1)
		e.mu.Unlock()
		return wait(ctx, f)
	}
	if now.Before(e.openUntil) || e.probe {
		e.stats.CircuitDenied.Add(1)
		e.mu.Unlock()
		return Result{}, ErrCircuit
	}
	if len(e.flights) >= e.cfg.MaxInflight {
		e.stats.Limited.Add(1)
		e.mu.Unlock()
		return Result{}, ErrBusy
	}
	if !e.rate.take(now) || !e.gate.Work(client) {
		e.stats.Limited.Add(1)
		e.mu.Unlock()
		return Result{}, ErrRate
	}
	if err := e.budget.Reserve(now); err != nil {
		e.stats.BudgetDenied.Add(1)
		e.mu.Unlock()
		return Result{}, err
	}
	// When a circuit cooldown expires, admit exactly one probe until it finishes.
	if e.failures >= e.cfg.CircuitFailures {
		e.probe = true
	}
	f := &flight{done: make(chan struct{}), probe: e.probe}
	e.flights[key] = f
	e.wg.Add(1)
	e.mu.Unlock()
	go e.run(key, f)
	return wait(ctx, f)
}
func wait(ctx context.Context, f *flight) (Result, error) {
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-f.done:
		return f.result, f.result.Err
	}
}
func (e *Engine) run(key string, f *flight) {
	defer e.wg.Done()
	ctx, cancel := context.WithTimeout(e.ctx, e.cfg.AITimeout.Value())
	defer cancel()
	e.stats.Calls.Add(1)
	text, err := e.provider.Answer(ctx, key)
	now := time.Now()
	ttl := e.cfg.CacheTTL.Value()
	if err != nil {
		ttl = e.cfg.FailureTTL.Value()
		e.stats.Failures.Add(1)
	}
	r := Result{Text: text, Err: err, Expires: now.Add(ttl)}
	e.mu.Lock()
	defer e.mu.Unlock()
	if f.probe {
		e.probe = false
	}
	if err == nil {
		// An older concurrent success must not cancel an active cooldown.
		if !e.probe && !now.Before(e.openUntil) {
			e.failures = 0
			e.openUntil = time.Time{}
		}
	} else {
		e.failures++
		cooldown := time.Duration(0)
		if e.failures >= e.cfg.CircuitFailures {
			cooldown = e.cfg.CircuitCooldown.Value()
		}
		var pe *ai.Error
		if errors.As(err, &pe) {
			cooldown = max(cooldown, pe.RetryAfter)
		}
		if cooldown > 0 {
			e.openUntil = maxTime(e.openUntil, now.Add(cooldown))
		}
		kind := "failure"
		if pe != nil {
			kind = pe.Kind
		}
		e.log.Warn("provider request failed", "kind", kind)
	}
	if !e.closed {
		if len(e.cache) >= e.cfg.CacheEntries {
			old := e.lru.Back()
			delete(e.cache, old.Value.(entry).key)
			e.lru.Remove(old)
		}
		e.cache[key] = e.lru.PushFront(entry{key, r})
	}
	f.result = r
	delete(e.flights, key)
	close(f.done)
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
func (e *Engine) Close() { e.mu.Lock(); e.closed = true; e.cancel(); e.mu.Unlock(); e.wg.Wait() }
func (e *Engine) Snapshot() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	day, calls, failed := e.budget.Snapshot()
	return map[string]any{"cache_entries": len(e.cache), "inflight": len(e.flights), "circuit_open": time.Now().Before(e.openUntil) || e.probe, "budget_day_utc": day, "budget_calls": calls, "budget_unavailable": failed, "daily_calls_limit": e.cfg.DailyCalls}
}
