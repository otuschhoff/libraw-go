// SPDX-License-Identifier: LGPL-2.1-only

package libraw

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otuschhoff/libraw-go/internal/bounded"
)

func rawContents(raw *RawImage) RawImage {
	if raw == nil {
		return RawImage{}
	}
	copy := *raw
	copy.account, copy.budget, copy.owner = nil, nil, nil
	return copy
}

func previewContents(preview *Preview) Preview {
	if preview == nil {
		return Preview{}
	}
	copy := *preview
	copy.account, copy.owner = nil, nil
	return copy
}

func testBudget(t testing.TB, bytes int64) *Budget {
	t.Helper()
	budget, err := NewBudget(bytes)
	if err != nil {
		t.Fatal(err)
	}
	return budget
}

func budgetUsed(b *Budget) int64 {
	_, used, _ := b.Stats()
	return used
}

func TestSafetyCanceledAndExpired(t *testing.T) {
	data, _, _ := withPreviews(t)
	file := openTest(t, data, OpenOptions{})
	defer file.Close()
	raw := renderRaw(16, 16)
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := error(context.Canceled)
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		for name, operation := range map[string]func() error{
			"open": func() error {
				f, err := Open(ctx, bytes.NewReader(data), int64(len(data)), OpenOptions{})
				if f != nil {
					t.Fatal("canceled Open returned a File")
				}
				return err
			},
			"preview": func() error {
				p, err := file.Preview(ctx)
				if p != nil {
					t.Fatal("canceled Preview returned bytes")
				}
				return err
			},
			"decode": func() error {
				r, err := file.DecodeRaw(ctx)
				if r != nil {
					t.Fatal("canceled DecodeRaw returned pixels")
				}
				return err
			},
			"render": func() error {
				r, err := Render(ctx, raw, RenderOptions{})
				if r != nil {
					t.Fatal("canceled Render returned pixels")
				}
				return err
			},
		} {
			if err := operation(); !errors.Is(err, want) {
				t.Fatalf("%s: %v, want %v", name, err, want)
			}
		}
		cancel()
	}
	if f, err := Open(nil, bytes.NewReader(data), int64(len(data)), OpenOptions{}); f != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatal("nil context accepted", err)
	}
}

// Pause at a deterministic poll after real work has started, then cancel the
// underlying standard context. There are no production hooks or sleeps.
type checkpointContext struct {
	context.Context
	polls   int
	at      int
	entered chan struct{}
	resume  chan struct{}
}

func (c *checkpointContext) Err() error {
	c.polls++
	if c.polls == c.at {
		prior := c.Context.Err()
		close(c.entered)
		<-c.resume
		// Simulate cancellation immediately after a successful poll, so latency
		// includes the work until the next bounded checkpoint.
		return prior
	}
	return c.Context.Err()
}

func cancelDuring(t *testing.T, at int, operation func(context.Context) error, reserved func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checkpoint := &checkpointContext{Context: ctx, at: at, entered: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(checkpoint.resume) })
	defer resume()
	done := make(chan error, 1)
	go func() { done <- operation(checkpoint) }()
	select {
	case <-checkpoint.entered:
	case err := <-done:
		t.Fatalf("operation finished before checkpoint %d: %v", at, err)
	case <-time.After(5 * time.Second):
		t.Fatal("operation did not reach checkpoint")
	}
	if !reserved() {
		cancel()
		resume()
		<-done
		t.Fatal("checkpoint did not have live allocations")
	}
	start := time.Now()
	cancel()
	resume()
	select {
	case err := <-done:
		latency := time.Since(start)
		t.Logf("cancellation latency %s (engineering target 50ms; CI ceiling 1s)", latency)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation not preserved: %v", err)
		}
		if latency > time.Second {
			t.Fatalf("cancellation exceeded CI ceiling: %s", latency)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation exceeded CI ceiling")
	}
}

func TestSafetyCancellationDuringWork(t *testing.T) {
	t.Run("parse", func(t *testing.T) {
		data := makeTestORF(false)
		budget := testBudget(t, 1<<20)
		cancelDuring(t, 20, func(ctx context.Context) error {
			file, err := Open(ctx, bytes.NewReader(data), int64(len(data)), OpenOptions{Budget: budget})
			if file != nil {
				file.Close()
				return errors.New("partial File returned")
			}
			return err
		}, func() bool { return budgetUsed(budget) > 2048 })
		if budgetUsed(budget) != 0 {
			t.Fatal("parse reservation leaked")
		}
		file := openTest(t, data, OpenOptions{Budget: budget})
		file.Close()
		if budgetUsed(budget) != 0 {
			t.Fatal("successful Open/Close leaked")
		}
	})
	for _, legacy := range []bool{false, true} {
		name := "word"
		data, _ := wordORF(512, 512, 512)
		if legacy {
			name = "legacy"
			payload, _ := encodeLegacy(legacyPlane(512, 512), 512, 512)
			data = legacyORF(512, 512, payload)
		}
		t.Run(name, func(t *testing.T) {
			budget := testBudget(t, 4<<20)
			file := openTest(t, data, OpenOptions{Budget: budget})
			defer file.Close()
			base := budgetUsed(budget)
			cancelDuring(t, 20, func(ctx context.Context) error {
				raw, err := file.DecodeRaw(ctx)
				if raw != nil {
					raw.Close()
					return errors.New("partial RAW returned")
				}
				return err
			}, func() bool { return budgetUsed(budget) > base })
			if budgetUsed(budget) != base {
				t.Fatal("decoder reservation leaked")
			}
			raw, err := file.DecodeRaw(context.Background())
			if err != nil {
				t.Fatal("reuse after cancellation", err)
			}
			raw.Close()
			if budgetUsed(budget) != base {
				t.Fatal("RAW close leaked")
			}
		})
	}
	t.Run("preview", func(t *testing.T) {
		data, _, _ := withPreviews(t)
		budget := testBudget(t, 1<<20)
		file := openTest(t, data, OpenOptions{Budget: budget})
		defer file.Close()
		base := budgetUsed(budget)
		cancelDuring(t, 6, func(ctx context.Context) error {
			preview, err := file.Preview(ctx)
			if preview != nil {
				preview.Close()
				return errors.New("partial preview returned")
			}
			return err
		}, func() bool { return budgetUsed(budget) > base })
		if budgetUsed(budget) != base {
			t.Fatal("preview reservation leaked")
		}
		preview, err := file.Preview(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		preview.Close()
	})
	t.Run("render", func(t *testing.T) {
		raw := renderRaw(1024, 1024)
		budget := testBudget(t, 16<<20)
		cancelDuring(t, 20, func(ctx context.Context) error {
			out, err := Render(ctx, raw, RenderOptions{Budget: budget})
			if out != nil {
				out.Close()
				return errors.New("partial rendered image returned")
			}
			return err
		}, func() bool { return budgetUsed(budget) > 8<<20 })
		if budgetUsed(budget) != 0 {
			t.Fatal("render reservation leaked")
		}
		out, err := Render(context.Background(), raw, RenderOptions{Budget: budget})
		if err != nil {
			t.Fatal("reuse after cancellation", err)
		}
		if budgetUsed(budget) != int64(len(out.Pix)) {
			t.Fatal("scratch was not released")
		}
		out.Close()
		out.Close()
		if budgetUsed(budget) != 0 {
			t.Fatal("render close leaked or double-released")
		}
	})
}

func TestSafetyBudgetsAndLifetimes(t *testing.T) {
	data, _ := wordORF(128, 128, 128)
	budget := testBudget(t, 1<<20)
	file := openTest(t, data, OpenOptions{Budget: budget})
	base := budgetUsed(budget)
	raw, err := file.DecodeRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if budgetUsed(budget) != base+int64(file.plan.outputBytes)+int64(len(raw.Diagnostics))*64 {
		t.Fatal("RAW retained output accounting mismatch")
	}
	file.Close()
	file.Close()
	if budgetUsed(budget) <= base {
		t.Fatal("shared metadata released while RAW still holds it")
	}
	raw.Close()
	raw.Close()
	if budgetUsed(budget) != 0 {
		t.Fatal("File/RAW lifecycle leaked")
	}
	if _, err := file.DecodeRaw(context.Background()); !errors.Is(err, ErrInvalidOptions) {
		t.Fatal("closed File decoded", err)
	}
	for _, size := range []int64{0, -1} {
		if _, err := NewBudget(size); !errors.Is(err, ErrInvalidOptions) {
			t.Fatal("invalid budget accepted")
		}
	}
	if f, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), OpenOptions{Budget: &Budget{}}); f != nil || !errors.Is(err, ErrInvalidOptions) {
		t.Fatal("zero budget accepted", err)
	}
	tiny := testBudget(t, 1)
	if f, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), OpenOptions{Budget: tiny}); f != nil || !errors.Is(err, ErrLimitExceeded) || budgetUsed(tiny) != 0 {
		t.Fatal("small budget did not reject parsing before allocation", err)
	}
	raw = renderRaw(64, 64)
	required := int64(64*64*8 + 64*24)
	tooSmall := testBudget(t, required-1)
	if out, err := Render(context.Background(), raw, RenderOptions{Budget: tooSmall}); out != nil || !errors.Is(err, ErrLimitExceeded) || budgetUsed(tooSmall) != 0 {
		t.Fatal("combined output+scratch budget not enforced", err)
	}
	raw.Width, raw.Height, raw.Stride = math.MaxInt, math.MaxInt, math.MaxInt
	if out, err := Render(context.Background(), raw, RenderOptions{Limits: Limits{Dimension: math.MaxInt64, Pixels: math.MaxInt64}}); out != nil || (!errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrLimitExceeded)) {
		t.Fatal("overflowing dimensions accepted", err)
	}
}

func TestSafetySharedPipelineBudget(t *testing.T) {
	data, _ := wordORF(128, 128, 128)
	probe := openTest(t, data, OpenOptions{})
	base := budgetUsed(probe.budget)
	probe.Close()
	// Each RAW allocation fits, but the second live plane does not.
	limit := base + 128*128*2 + 128*2 + 4096
	budget := testBudget(t, limit)
	file := openTest(t, data, OpenOptions{Budget: budget})
	defer file.Close()
	raw, err := file.DecodeRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if other, err := file.DecodeRaw(context.Background()); other != nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatal("simultaneously live RAW planes not charged together", err)
	}
	raw.Close()
	raw, err = file.DecodeRaw(context.Background())
	if err != nil {
		t.Fatal("reservation not reusable", err)
	}
	raw.Close()

	// File-produced RAW inherits its budget when rendering.
	pipelineBudget := testBudget(t, base+128*128*8+128*24)
	pipeline := openTest(t, data, OpenOptions{Budget: pipelineBudget})
	defer pipeline.Close()
	raw, err = pipeline.DecodeRaw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.ActiveArea, raw.CFA, raw.Calibration, raw.Orientation = renderRaw(128, 128).ActiveArea, renderRaw(2, 2).CFA, renderRaw(2, 2).Calibration, OrientationTopLeft
	if out, err := Render(context.Background(), raw, RenderOptions{}); out != nil || !errors.Is(err, ErrLimitExceeded) {
		t.Fatal("RAW and render output not charged together", err)
	}
}

func TestSafetyConcurrentBudget(t *testing.T) {
	raw := renderRaw(128, 128)
	required := int64(128*128*8 + 128*24)
	budget := testBudget(t, required)
	start := make(chan struct{})
	results := make(chan *RenderedImage, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := Render(context.Background(), raw, RenderOptions{Budget: budget})
			results <- out
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	successes, failures := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrLimitExceeded) {
			failures++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || failures != 7 {
		t.Fatalf("budget admitted %d operations, rejected %d", successes, failures)
	}
	for out := range results {
		out.Close()
	}
	_, used, peak := budget.Stats()
	if used != 0 || peak != required {
		t.Fatalf("shared accounting: used=%d peak=%d", used, peak)
	}
}

func TestSafetyErrorCleanup(t *testing.T) {
	budget := testBudget(t, 1<<20)
	raw := renderRaw(32, 32)
	raw.Pixels[5] = 65535
	if out, err := Render(context.Background(), raw, RenderOptions{Budget: budget}); out != nil || !errors.Is(err, ErrCorrupt) || budgetUsed(budget) != 0 {
		t.Fatal("render error leaked reservations", err)
	}
	raw.Pixels[5] = 0
	out, err := Render(context.Background(), raw, RenderOptions{Budget: budget})
	if err != nil {
		t.Fatal("budget not reusable", err)
	}
	out.Close()
	data := makeTestORF(false)
	data[12], data[13] = 0, 0 // Unknown TIFF type fails after parser reservations.
	if f, err := Open(context.Background(), bytes.NewReader(data), int64(len(data)), OpenOptions{Budget: budget}); f != nil || err == nil || budgetUsed(budget) != 0 {
		t.Fatal("parse error leaked reservations", err)
	}
	data, _ = wordORF(32, 32, 32)
	reader := &rawReader{input: bytes.NewReader(data)}
	file, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	base := budgetUsed(budget)
	reader.err = io.ErrUnexpectedEOF
	if raw, err := file.DecodeRaw(context.Background()); raw != nil || !errors.Is(err, io.ErrUnexpectedEOF) || budgetUsed(budget) != base {
		t.Fatal("decode I/O error leaked", err)
	}
	reader.err = nil
	decoded, err := file.DecodeRaw(context.Background())
	if err != nil {
		t.Fatal("decode budget not reusable after I/O error", err)
	}
	decoded.Close()
	retained := budgetUsed(budget)
	data, _, _ = withPreviews(t)
	reader = &rawReader{input: bytes.NewReader(data)}
	previewFile, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	base = budgetUsed(budget)
	reader.err = io.ErrUnexpectedEOF
	if preview, err := previewFile.Preview(context.Background()); preview != nil || !errors.Is(err, io.ErrUnexpectedEOF) || budgetUsed(budget) != base {
		t.Fatal("preview I/O error leaked", err)
	}
	reader.err = nil
	preview, err := previewFile.Preview(context.Background())
	if err != nil {
		t.Fatal("preview budget not reusable", err)
	}
	previewFile.Close()
	if budgetUsed(budget) <= base {
		t.Fatal("preview prematurely released shared metadata")
	}
	preview.Close()
	preview.Close()
	if budgetUsed(budget) != retained {
		t.Fatal("preview close leaked")
	}
}

type blockedReader struct {
	*bytes.Reader
	entered, resume chan struct{}
	once            sync.Once
}

func (r *blockedReader) ReadAt(p []byte, offset int64) (int, error) {
	if offset >= 10 {
		r.once.Do(func() {
			close(r.entered)
			<-r.resume
		})
	}
	return r.Reader.ReadAt(p, offset)
}

func TestSafetyBlockingReadLimitation(t *testing.T) {
	data := makeTestORF(false)
	reader := &blockedReader{Reader: bytes.NewReader(data), entered: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(reader.resume) })
	defer resume()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	budget := testBudget(t, 1<<20)
	done := make(chan error, 1)
	go func() {
		file, err := Open(ctx, reader, int64(len(data)), OpenOptions{Budget: budget})
		if file != nil {
			file.Close()
		}
		done <- err
	}()
	select {
	case <-reader.entered:
	case err := <-done:
		t.Fatalf("Open finished before entering the blocking read: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Open did not enter the blocking read")
	}
	cancel()
	select {
	case err := <-done:
		resume()
		t.Fatalf("arbitrary blocked ReadAt was unexpectedly interrupted: %v", err)
	default:
	}
	resume()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || budgetUsed(budget) != 0 {
			t.Fatal("cancellation after blocked ReadAt lost or leaked", err)
		}
	case <-time.After(time.Second):
		t.Fatal("operation did not cancel after ReadAt unblocked")
	}
	file, err := Open(context.Background(), reader, int64(len(data)), OpenOptions{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
}

func TestSafetyRejectsBeforeLargeAllocation(t *testing.T) {
	raw := renderRaw(1024, 1024) // An 8 MiB library output would be required.
	budget := testBudget(t, 1024)
	result := testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			out, err := Render(context.Background(), raw, RenderOptions{Budget: budget})
			if out != nil || !errors.Is(err, ErrLimitExceeded) {
				b.Fatal("small budget accepted render", err)
			}
		}
	})
	if bytes := result.AllocedBytesPerOp(); bytes > 32<<10 {
		t.Fatalf("rejected render allocated %d bytes/op before reporting limit", bytes)
	}
	_, used, peak := budget.Stats()
	if used != 0 || peak != 0 {
		t.Fatal("failed reservation changed budget")
	}
}

func TestSafetyDeadlineDuringParsing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		checkpoint := &checkpointContext{Context: ctx, at: 20, entered: make(chan struct{}), resume: make(chan struct{})}
		data := makeTestORF(false)
		budget := testBudget(t, 1<<20)
		done := make(chan error, 1)
		go func() {
			file, err := Open(checkpoint, bytes.NewReader(data), int64(len(data)), OpenOptions{Budget: budget})
			if file != nil {
				file.Close()
				done <- errors.New("expired parse returned a File")
				return
			}
			done <- err
		}()
		<-checkpoint.entered
		if budgetUsed(budget) <= 2048 {
			close(checkpoint.resume)
			t.Fatal("deadline checkpoint preceded parsing allocations")
		}
		time.Sleep(2 * time.Second) // Virtual time: no wall-clock wait.
		close(checkpoint.resume)
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) || budgetUsed(budget) != 0 {
			t.Fatal("deadline after parsing started was lost or leaked", err)
		}
	})
}

func TestSafetyConcurrentOutputClose(t *testing.T) {
	data, _ := wordORF(64, 64, 64)
	budget := testBudget(t, 1<<20)
	file := openTest(t, data, OpenOptions{Budget: budget})
	results := make(chan *RawImage, 4)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, err := file.DecodeRaw(context.Background())
			if err != nil {
				t.Error(err)
			}
			results <- raw
			<-release
			raw.Close()
		}()
	}
	for range 4 {
		if raw := <-results; raw == nil {
			close(release)
			wg.Wait()
			t.Fatal("shared budget rejected a fitting concurrent RAW")
		}
	}
	// All File operations are finished. Independent output handles may close
	// concurrently with File, releasing their shared metadata ownership.
	close(release)
	file.Close()
	wg.Wait()
	if budgetUsed(budget) != 0 {
		t.Fatal("concurrent metadata/output ownership leaked")
	}
}

func TestSafetyExtentSorting(t *testing.T) {
	source, err := bounded.New(bytes.NewReader(nil), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	p := parser{source: source}
	for _, count := range []int{0, 1, 2, 3, 257, 4097} {
		spans := make([]fileExtent, count)
		for i := range spans {
			spans[i].offset = int64(count - i)
		}
		if err := p.sortExtents(spans); err != nil {
			t.Fatal(err)
		}
		for i, span := range spans {
			if span.offset != int64(i+1) {
				t.Fatal("descriptor merge sort changed ordering")
			}
		}
	}
	ctx := &renderCancelContext{Context: context.Background(), after: 5}
	p.source = source.WithContext(ctx)
	if err := p.sortExtents(make([]fileExtent, 4097)); !errors.Is(err, context.Canceled) {
		t.Fatal("descriptor sort did not cancel", err)
	}
}
