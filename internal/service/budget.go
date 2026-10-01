package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var ErrBudget = errors.New("daily AI call budget exhausted or unavailable")

type budgetState struct {
	Day   string `json:"day"`
	Calls int    `json:"calls"`
}
type Budget struct {
	mu     sync.Mutex
	path   string
	limit  int
	state  budgetState
	lock   *os.File
	failed bool
}

func OpenBudget(path string, limit int) (*Budget, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("budget file is locked by another process")
	}
	b := &Budget{path: path, limit: limit, lock: lock, state: budgetState{Day: time.Now().UTC().Format("2006-01-02")}}
	f, e := os.Open(path)
	if e == nil {
		dec := json.NewDecoder(io.LimitReader(f, 4097))
		dec.DisallowUnknownFields()
		var disk struct {
			Day   *string `json:"day"`
			Calls *int    `json:"calls"`
		}
		e = dec.Decode(&disk)
		if disk.Day == nil || disk.Calls == nil {
			e = errors.New("missing budget fields")
		} else {
			b.state = budgetState{*disk.Day, *disk.Calls}
		}
		if e == nil && dec.Decode(new(any)) != io.EOF {
			e = errors.New("extra budget data")
		}
		f.Close()
		t, te := time.Parse("2006-01-02", b.state.Day)
		if e != nil || te != nil || t.Format("2006-01-02") != b.state.Day || b.state.Calls < 0 {
			b.Close()
			return nil, errors.New("invalid budget state; refusing to reset it")
		}
	}
	if e != nil && !os.IsNotExist(e) {
		b.Close()
		return nil, e
	}
	if os.IsNotExist(e) {
		if e = b.persist(); e != nil {
			b.Close()
			return nil, e
		}
	}
	return b, nil
}

// Reserve durably counts attempts, including failures. Never refund an uncertain
// request: a timeout/disconnect does not prove the provider did not charge it.
func (b *Budget) Reserve(now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failed || b.lock == nil {
		return ErrBudget
	}
	day := now.UTC().Format("2006-01-02")
	if day < b.state.Day {
		return ErrBudget
	}
	if day > b.state.Day {
		b.state = budgetState{Day: day}
	}
	if b.state.Calls >= b.limit {
		return ErrBudget
	}
	b.state.Calls++
	if e := b.persist(); e != nil {
		b.failed = true
		return fmt.Errorf("%w: persistence failed", ErrBudget)
	}
	return nil
}
func (b *Budget) persist() error {
	dir := filepath.Dir(b.path)
	f, e := os.CreateTemp(dir, ".budget-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = json.NewEncoder(f).Encode(b.state); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Rename(name, b.path); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}
func (b *Budget) Snapshot() (string, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state.Day, b.state.Calls, b.failed
}
func (b *Budget) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lock == nil {
		return nil
	}
	e := b.lock.Close()
	b.lock = nil
	return e
}
