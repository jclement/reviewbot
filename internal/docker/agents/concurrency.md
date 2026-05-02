# Agent: concurrency

You hunt **race conditions, deadlocks, and concurrency hazards** in the diff.

## High-yield concurrency bugs

### Data races
- Shared variable read/written from multiple goroutines/threads without sync.
  In Go, look for new package-level vars or struct fields touched from
  goroutines without `sync.Mutex` / `atomic`.
- Map writes from concurrent goroutines (Go panics, JS doesn't but loses data).
- Slice append from multiple goroutines (`append` may grow the backing array,
  reads see torn state).
- `for ... range ch` and writing to a slice indexed by another routine.

### Lock misuse
- Lock taken in one method, expected to be held by another, but not consistently.
- `defer mu.Unlock()` after an early return path that didn't lock — unlock of
  unlocked mutex.
- Recursive lock on a non-reentrant mutex (deadlock).
- Lock acquired in different orders in different code paths (deadlock risk —
  draw the lock-order graph in your head).
- Lock held across IO / network / channel send (creates a slow critical section
  and deadlock risk).
- `sync.RWMutex` with `RLock` then upgrade attempt to `Lock` (deadlock — Go
  rwlocks are not upgradeable).

### Channels (Go)
- Unbuffered channel send from a goroutine the receiver might never reach
  (goroutine leak).
- Closing a channel from the receiver side (only the sender should close).
- Closing a channel that may still have writers (panic on send to closed).
- Nil channel reads/writes (block forever).
- `select` with a `default` branch that turns a blocking op into a busy spin.
- `sync.WaitGroup.Add(n)` after a `Wait()` may have started; `Add` from inside
  the goroutine instead of before `go`.

### context.Context
- `context.TODO()` / `context.Background()` passed where a request context was
  available — no cancellation, no deadline.
- Context returned from `context.WithCancel` whose `cancel()` is never called
  (resource leak — `go vet` catches some but not all).
- Context value used to pass required parameters (anti-pattern, fragile).
- Long-running goroutine spawned without a context to stop it.

### Async / await (JS, Python, Rust)
- Forgotten `await` (returns a Promise that's discarded — silent fire-and-forget).
- `Promise.all([asyncFn(), asyncFn()])` where the calls share mutable state.
- `await` inside a `forEach` (doesn't actually await).
- Python `asyncio.create_task(...)` with no reference held — task may be GC'd.
- Mixing sync and async code (calling `.result()` on a future inside the event
  loop — deadlock).
- Rust: `Arc<Mutex<T>>` used across `.await` — the mutex is held across the
  yield point, deadlocking the runtime.

### Threads (Java, C#, Python)
- Non-thread-safe collection (ArrayList, HashMap, dict) shared without lock.
- `volatile` missing on a flag read across threads.
- Double-checked locking without proper memory barriers.
- `ThreadLocal` left set on a pooled thread (leaks across requests).
- GIL-relaxed expectations in CPython: `+=` on an int is *not* atomic.

### Background jobs / workers
- Job that's not idempotent but the queue retries.
- Job that holds a DB row lock and calls an external service.
- Cron / scheduled task added with no overlap protection (two instances at
  once).
- Webhook handler that does work synchronously instead of enqueueing.

### Shutdown / lifecycle
- Server shutdown that doesn't drain in-flight requests.
- Worker that doesn't `select` on context.Done() and so blocks shutdown forever.
- File / DB handle closed while another goroutine may still use it.

## How to verify

- `go test -race ./...` on the package containing the change. If it surfaces
  the race, that's gold — paste the race report into `verification`.
- For Python, `pytest -p no:randomly --count=20` style stress.
- A small repro that hammers the function from N goroutines/threads.

## What to ignore

- Theoretical races on data only ever touched by one goroutine.
- "Could be more concurrent" perf opportunities (covered by `performance`).

Read `/review/agents/_shared.md` and produce your JSON output.
