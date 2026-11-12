// SPDX-License-Identifier: LGPL-2.1-only

package bounded

import (
	"sync"

	"github.com/otuschhoff/libraw-go/internal/fault"
)

// Budget counts explicit live buffer reservations, not Go heap usage.
type Budget struct {
	mu                sync.Mutex
	limit, used, peak uint64
}

func NewBudget(limit uint64) *Budget { return &Budget{limit: limit} }

func (b *Budget) Stats() (limit, used, peak uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit, b.used, b.peak
}

func (b *Budget) reserve(n uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.limit-b.used {
		return &fault.Error{Op: "reserve memory", Kind: fault.Limit, Detail: "shared live-buffer budget exceeded"}
	}
	b.used += n
	b.peak = max(b.peak, b.used)
	return nil
}

func (b *Budget) release(n uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
}

// Account adds a per-operation quota to a shared budget. Its remaining charge
// can be transferred to a returned handle and released by an idempotent Close.
type Account struct {
	budget *Budget
	limit  uint64
	used   uint64
	closed bool
	mu     sync.Mutex
}

func NewAccount(budget *Budget, limit uint64) *Account {
	return &Account{budget: budget, limit: limit}
}

func (a *Account) Reserve(n uint64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || n > a.limit-a.used {
		return &fault.Error{Op: "reserve memory", Kind: fault.Limit, Detail: "operation working-buffer budget exceeded"}
	}
	if err := a.budget.reserve(n); err != nil {
		return err
	}
	a.used += n
	return nil
}

func (a *Account) Release(n uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.budget.release(n)
	a.used -= n
}

func (a *Account) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		a.budget.release(a.used)
		a.used = 0
		a.closed = true
	}
	return nil
}
