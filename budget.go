// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"image"
	"sync"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

// RAW/preview metadata contains immutable strings shared with File. Keep the
// conservative parse charge until the last dependent handle is closed.
type metadataOwner struct {
	mu      sync.Mutex
	refs    int
	account *bounded.Account
}

func (o *metadataOwner) retain() {
	if o != nil {
		o.mu.Lock()
		o.refs++
		o.mu.Unlock()
	}
}

func (o *metadataOwner) release() {
	if o != nil {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.refs--
		if o.refs == 0 {
			o.account.Close()
		}
	}
}

// Budget is a race-safe, fail-fast quota for live library buffer reservations.
// Share one across Open and Render calls to bound a pipeline or worker pool.
// It is not a Go heap or RSS limit. The zero value is invalid; use NewBudget.
type Budget struct {
	state *bounded.Budget
}

// NewBudget creates a positive aggregate live-buffer quota.
func NewBudget(bytes int64) (*Budget, error) {
	if bytes <= 0 {
		return nil, &Error{Op: "budget", Kind: ErrInvalidOptions, Detail: "budget must be positive"}
	}
	return &Budget{state: bounded.NewBudget(uint64(bytes))}, nil
}

// Stats returns the byte limit, current reservations and peak reservations.
func (b *Budget) Stats() (limit, used, peak int64) {
	if b == nil || b.state == nil {
		return 0, 0, 0
	}
	l, u, p := b.state.Stats()
	return int64(l), int64(u), int64(p)
}

func operationAccount(b *Budget, limit int64) (*Budget, *bounded.Account, error) {
	if b == nil {
		b, _ = NewBudget(limit) // resolved Limits are positive
	}
	if b.state == nil {
		return nil, nil, &Error{Op: "budget", Kind: ErrInvalidOptions, Detail: "uninitialized Budget"}
	}
	return b, bounded.NewAccount(b.state, uint64(limit)), nil
}

// RenderedImage owns a standard Go image and its reservation. Close invalidates
// it and all aliases of its pixels. Do not copy handles or close them while in
// use. Close releases accounting, not necessarily memory retained by Go's GC.
type RenderedImage struct {
	*image.NRGBA64
	account *bounded.Account
}

func (r *RenderedImage) Close() error {
	if r == nil {
		return nil
	}
	r.NRGBA64 = nil
	return r.account.Close()
}

func (r *RawImage) Close() error {
	if r == nil {
		return nil
	}
	r.Pixels = nil
	r.Diagnostics = nil
	r.Calibration = Calibration{}
	if r.owner != nil {
		r.owner.release()
		r.owner = nil
	}
	return r.account.Close()
}

func (p *Preview) Close() error {
	if p == nil {
		return nil
	}
	p.JPEG = nil
	p.Diagnostics = nil
	p.Orientation = Value[Orientation]{}
	if p.owner != nil {
		p.owner.release()
		p.owner = nil
	}
	return p.account.Close()
}
