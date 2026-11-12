# In-process cancellation, budgets and ownership

These contracts supersede the historical phase reports' ownership and
per-call-only accounting descriptions. They improve in-process safety, but
**do not replace subprocess isolation** against fatal runtime failures or all
forms of memory exhaustion.

## API and migration from v0.1.0

- `Open(ctx, input, size, opts)` now requires a non-nil `context.Context`.
  `Preview`, `DecodeRaw` and `Render` retain their context parameters.
- `OpenOptions.Budget` and `RenderOptions.Budget` accept a shared `*Budget`,
  created with `NewBudget(positiveBytes)`. Its zero value is invalid.
- Close every successful `File`, `RawImage`, `Preview` and `RenderedImage`.
  `Close` is idempotent and does not close the borrowed input.
- `Render` now returns `*RenderedImage`, embedding `*image.NRGBA64`. It still
  implements `image.Image`; pass it directly to `png.Encode`, or access
  `result.NRGBA64` when a concrete standard image is needed.
- `Metadata()` returns a value with borrowed, read-only diagnostics, valid
  while the File is open. `Metadata().Clone()` explicitly creates a
  caller-owned snapshot outside budget accounting. Immutable string storage
  can be shared by snapshots.

Do not copy ownership handles by value. Do not mutate their fields/slices,
replace their backing storage, or close them concurrently with any use.
Caller-constructed RAW images remain supported: their input storage is
caller-owned and is not retrospectively charged to a library budget.

## Cooperative cancellation

All four expensive operations reject canceled contexts and expired deadlines,
and return nil results with errors preserving `errors.Is` for
`context.Canceled` and `context.DeadlineExceeded`. Errors and cancellation
release that operation's scratch/output reservations before returning.
Previously returned handles remain valid and charged until closed.

Cancellation checks occur:

- Before and after input reads, split into requests of at most 32 KiB.
- During TIFF entry materialization, directory traversal, payload extent
  validation and merge sorting.
- Every 4096 bytes during ASCII validation/trimming and every 32 KiB during
  string copying.
- At row boundaries and every 256 samples in word and predictive RAW loops,
  including coded/discarded columns, and during normalization/demosaicing.
- During preview byte copying and JPEG header reads, and before success.

The engineering latency target is **50 ms after cancellation**, for responsive
inputs on the recorded workloads. Deterministic tests cancel immediately after
a successful poll during real work; a **1-second CI ceiling** allows scheduling,
race instrumentation and loaded machines. This is measured behavior, not a
hard real-time deadline.

An arbitrary `ReaderAt` already blocked in `ReadAt` cannot be interrupted by
a context check. The operation retains its reservations until that read
returns, then observes cancellation. No helper goroutines are spawned to
race or abandon reads. Applications needing interruptible I/O must provide it
at the input layer. Allocation/zeroing, GC/scheduling pauses, small fixed-size
standard-library helpers and error formatting are also not interruptible.
There is a final cancellation check, not an atomic transaction with a
concurrently canceling caller; cancellation after that check may race success.

## Per-operation quotas and aggregate reservations

`Limits` still bounds input extent, dimensions, pixels, directory counts/depth,
entries, cumulative metadata materialization, preview bytes and working bytes.
Zero selects existing defaults; negative values and contradictory limits are
invalid. `MetadataBytes` and `PreviewBytes` must not exceed `WorkingBytes`.
`InputBytes` bounds the declared extent, not allocated/copied input: production
code borrows `ReaderAt` and never copies a whole input.

`WorkingBytes` bounds an operation's accounted allocations, including its
returned output. A `Budget` additionally bounds **all simultaneously live
reservations using that same budget**, across stages, handles and concurrent
operations. It rejects immediately with `ErrLimitExceeded` rather than waiting
for another operation to release memory. `Stats()` atomically reports limit,
current reservations and peak reservations.

If Open has no explicit Budget, it creates one of size `WorkingBytes`.
All its decode/preview calls inherit that budget. Library-produced RAW images
pass it to Render unless `RenderOptions.Budget` explicitly selects another.
Standalone rendering of caller-constructed RAW creates its own budget when
none is supplied. Distinct budgets do not coordinate with one another.
Share one budget across separate Open calls to bound a worker pool.
Capabilities describe layout/calibration and static per-operation quotas;
they do not guarantee that a shared budget currently has enough free capacity.

| Allocation | Accounting and lifetime |
|---|---|
| TIFF entry tables, selected tag bytes, copied strings, parser maps/slices, directories, extents, plans and diagnostics | Reserved before allocation against cumulative `MetadataBytes`, operation `WorkingBytes` and shared Budget, with conservative structural/capacity allowances. No refunds for discarded parser data. The conservative parse charge remains until File and its last dependent RAW/preview handle close. |
| JPEG header parsing in Open | A transient 64 KiB conservative standard-library scratch allowance, live alongside metadata; released after each header attempt. No pixel decompression. |
| Preview output | Original JPEG length plus diagnostic-slice allowance; held until Preview.Close. Also constrained by PreviewBytes. |
| RAW decoding | Full `2*width*height` output, input row or 4096-byte predictive scratch, and `64*diagnosticCount` bytes checked/reserved together. Scratch is released on return; output/diagnostics until RawImage.Close. |
| Rendering | `8*cropWidth*cropHeight` output plus `24*cropWidth` three-row scratch, reserved together before allocation. Scratch is released on return; output until RenderedImage.Close. The inherited budget still holds the RAW plane. |

Checked multiplication/addition and platform-int checks precede variable-size
allocations. Append-driven parser storage uses conservative capacity-growth
allowances; strings are copied into pre-sized storage. Predictive decompression
writes directly into the pre-sized sensor plane, without an append-driven or
whole-strip expansion buffer. Descriptor sorting has one pre-accounted scratch
array, rather than unbounded growth.

Returned output does not have a separate, exempt output pool: it counts toward
WorkingBytes when produced and toward Budget until closed. RAW storage is not
charged a second time to Render's per-operation quota. RAW plus render output
plus scratch are nevertheless simultaneous reservations in the inherited
Budget. Closing File before RAW/preview is safe; shared immutable metadata
keeps its reservation. Rendered pixels are independent of RAW after return.

Close invalidates all aliases obtained from that handle, including standard
image views, subslices and subimages. Keeping or copying those aliases after
Close, or replacing library-owned backing slices, violates the accounting
contract. Caller-made copies must be budgeted separately by the application.
Failing to Close retains reservations; there are no GC finalizers that guess
ownership lifetimes.

### What this does not bound

Reservations are **not exact Go heap accounting or an OS-enforced memory cap**.
Object/slice/string headers, allocator size-class rounding, stacks, maps beyond
their conservative allowances, GC retention, runtime internals, caller-owned
inputs/RAW/copies/snapshots, custom ReaderAt behavior/error strings and allocations
in downstream encoders or other dependencies are not exactly accounted.
The fixed JPEG allowance is conservative, not an allocator hook or promise
about every future Go implementation. Freed logical reservations need not
immediately reduce heap use or RSS. Resource limits do not recover from
runtime OOM, fatal panics, bugs or hostile input behavior in caller code.
No process-global runtime memory settings are used.

## In-process pipeline

The same budget can be constructed once and shared across concurrent jobs.
Choose aggregate capacity for all retained outputs; limits are quotas, not
preallocations. Encoding below is caller-controlled and outside the library's
budget and cancellation contract.

```go
func exportPNG(parent context.Context, input io.ReaderAt, size int64, dst io.Writer) error {
    ctx, cancel := context.WithTimeout(parent, 5*time.Second)
    defer cancel()
    budget, err := libraw.NewBudget(256 << 20)
    if err != nil {
        return err
    }
    limits := libraw.DefaultLimits()
    limits.WorkingBytes = 192 << 20
    limits.MetadataBytes = 8 << 20
    limits.PreviewBytes = 32 << 20
    file, err := libraw.Open(ctx, input, size, libraw.OpenOptions{
        Limits: limits, Budget: budget,
    })
    if err != nil {
        return err
    }
    defer file.Close()
    raw, err := file.DecodeRaw(ctx)
    if err != nil {
        return err
    }
    defer raw.Close()
    rgb, err := libraw.Render(ctx, raw, libraw.RenderOptions{Limits: limits})
    if err != nil {
        return err
    }
    defer rgb.Close()
    return png.Encode(dst, rgb)
}
```

Canceling `parent` cancels library work. Handle context errors with `errors.Is`;
handle allocation rejection with `errors.Is(err, libraw.ErrLimitExceeded)`.
Keep the input immutable and open through File operations. Budget sharing
and statistics are race-safe; Close must not overlap any use of its handle.

## Validation and reproduction

Focused synthetic and full-resolution public-fixture tests cover pre-canceled
and expired contexts, cancellation after work/allocations start, the blocking
read limitation, aggregate concurrent rejection, RAW/render coexistence,
idempotent cleanup and successful reuse after cancellation/I/O/parser/pixel
errors. A rejected 8 MiB render must allocate under 32 KiB per call, measured
with the Go benchmark allocator counters, rather than merely report a limit.
Existing exact RAW and complete rendered-baseline comparisons remain in place.

```sh
CGO_ENABLED=0 go test -tags=integration -run='TestSafety|TestReferenceCooperativeCancellation' -count=1 -v .
go test -race -tags=integration -run='TestSafety|TestReferenceCooperativeCancellation|TestRenderQuotasCancellationAndOwnership|TestWordOwnership' -count=1 ./...
CGO_ENABLED=0 go test -tags=integration -run='^$' -bench='Benchmark(Reference(Open|Decode|Render)|DecodeWord)$' -benchmem -benchtime=200ms -count=3 .
```

Public integration tests require the existing licensed fixtures; see the
[reference workflow](../tools/reference/README.md). No private photographs
are required or uploaded.

### Recorded results (2026-11-12)

Go 1.27.3, macOS/arm64, Apple M1 Max: the full integration suite, vet,
CGO-disabled build, reference-tool tests and offline artifact verification
passed. Focused safety/example tests also passed on minimum Go 1.26.0.
Focused race tests passed, including all four full-resolution public
fixtures. Observed reference-work cancellation latency under the race detector
was **6-255 microseconds**, below the 50 ms target and 1-second CI ceiling.
Linux/386 integration packages also cross-compiled; that check did not execute
32-bit code.

Successful-operation benchmarks used the same fixture inputs, three samples
and `-benchtime=200ms` before/after. Medians include explicit Close in the new
API. These short local runs quantify observed cost, not statistically proven
speedups; timings varied with machine state.

| Workload | Before | After | Observed change |
|---|---:|---:|---:|
| E-400 Open | 71.6 us | 74.1 us | +3.6% |
| E-1 word RAW decode | 8.80 ms | 7.82 ms | -11.1% |
| E-410 predictive RAW decode | 420.90 ms | 368.46 ms | -12.5% |
| E-410 full render | 1569.12 ms | 1480.24 ms | -5.7% |
| 31x31 synthetic word decode | 1.8 us | 1.9 us | +5.2% |

Across all four fixtures, Open medians changed -6.7% to +3.6%, RAW decode
-13.0% to -7.2%, and rendering -8.9% to -5.7%. The clearer accounting overhead
is allocation cost: Open added roughly 10-15 KiB and 5-6 allocations
(including per-entry context references); RAW added roughly 80–128 bytes and
1-2 allocations; rendering added 64 bytes and two allocations per call.
There was no additional image-sized buffer or per-row allocation.
