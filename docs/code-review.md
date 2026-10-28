# Code review — 2026-10-28

A review of the code as it is written, rather than of the defects it contains. The audit ([`audit.md`](audit.md)) covered
what is broken; this covers what will make the code expensive to live with.

Every claim below was checked against the code. Where a claim was testable, it was tested, and the evidence is given.

**Shape**: 4,654 lines of Go across 6 packages, 1,300 lines of TS/TSX, no third-party abstractions in the critical path.

---

## Verdict

This is a good codebase with a handful of specific weaknesses. The structure is right: the domain is behind a one-method
interface, the persistence is behind PocketBase, and the frontend talks through a single typed seam. The tests are
behavioural and use a real database. The errors are wrapped with context.

The weaknesses cluster in one place: **the binding layer and the manager have accumulated API that nothing uses**, and each
piece of unused API is a place where the code can drift from what it claims. One of them publishes internal objects to the
frontend.

Nothing here is architecturally wrong. Most of it is a few hours of deletion and tightening.

---

## What is genuinely good

These are worth naming, because they are the things a later change must not undo.

| Quality | Where |
| --- | --- |
| **A one-method extension point.** `Runner` has a single method. The whole domain plugs in there, and nothing else has to change. | `internal/jobs/types.go:121` |
| **No mocks.** The tests bootstrap a real SQLite database in a temporary directory and exercise the real query paths. A mock would have hidden the `RetryJob` data loss; the real database caught it. | `internal/jobs/manager_test.go:13` |
| **Concurrency that is actually correct.** Fifty concurrent submits under `-race`, and `WithConcurrency` measured to honour its limit exactly. | `internal/jobs/manager_test.go`, `lifecycle_test.go` |
| **Errors carry context.** `fmt.Errorf("...: %w", err)` is used consistently, so a failure names the operation that failed. | throughout |
| **The typed seam.** No component imports a generated binding; the wrapper normalises the nullable arrays once, at the boundary. | `frontend/src/bindings/index.ts` |
| **The queue's shape.** A buffered channel as a semaphore is the idiomatic way to bound concurrency in Go, and it is used correctly. | `internal/jobs/manager.go:463` |

---

## Findings

### 1. Internal objects are published to the frontend API — *must fix*

`internal/bindings/service.go:38-44`

```go
func (s *AppService) JobManager() *jobs.Manager          { return s.jobs }
func (s *AppService) Console() *console.Log              { return s.console }
func (s *AppService) Settings() *pocketbase.SettingsStore { return s.settings }
```

Every exported method on a bound service becomes a method in the generated TypeScript. These three produce:

```ts
export function JobManager(): $CancellablePromise<jobs$0.Manager | null>
export function Console(): $CancellablePromise<console$0.Log | null>
export function Settings(): $CancellablePromise<pocketbase$0.SettingsStore | null>
```

**Nothing calls them.** Not `main.go`, not the frontend, not the tests — verified by search.

And they cannot work. `*jobs.Manager` holds a mutex, a channel, a cancellation map and a database handle, all unexported, so
it serialises to `{}`. So does `*pocketbase.SettingsStore`. The frontend is offered three methods that return an empty
object, and `SettingsStore` is the type that owns `GetAPIKey`.

To be precise about the risk: **no secret is reachable this way.** The frontend receives a serialised value, not a bound
service, so it cannot call a method on it. This is an API-surface defect, not a leak.

It is still the worst thing in the codebase, because it is a lie told to every future reader of the generated bindings: *there
is a `Settings` method; what does it do?* The answer is nothing.

**Fix**: delete all three. They exist because an earlier version needed to reach the manager from `main.go`, and that
coupling is gone.

**Prevent the recurrence**: add a test that asserts the exact method set of `AppService`. There is none, which is why three
dead methods survived.

---

### 2. The job context leaks — *must fix*

`internal/jobs/manager.go:489`

```go
ctx, cancel := context.WithCancel(m.rootCtx)
if m.timeout > 0 {
    ctx, cancel = context.WithTimeout(m.rootCtx, m.timeout)
}
```

When a timeout is set — which is the default, thirty minutes — the second line runs and **overwrites `cancel`**. The cancel
function of the first context is dropped, so that context is never cancelled. It remains registered as a child of `rootCtx`
until the application shuts down.

One leaked context per job, for the lifetime of the process.

`go vet -lostcancel` does **not** catch this (verified — it reports nothing), because the variable is reassigned rather than
ignored. That is why it survived the audit.

The cleaner shape creates exactly one context:

```go
func (m *Manager) jobContext() (context.Context, context.CancelFunc) {
    if m.timeout > 0 {
        return context.WithTimeout(m.rootCtx, m.timeout)
    }
    return context.WithCancel(m.rootCtx)
}
```

---

### 3. A joined error loses its code — *must fix*

`internal/jobs/manager.go:613`

`asError` is a hand-rolled `errors.As` that walks `Unwrap() error`. Since Go 1.20 an error can also implement
`Unwrap() []error`, which is what `errors.Join` returns. The loop's type assertion fails, the coded error is not found, and
the job loses its numeric code.

Measured:

```
fmt.Errorf wrap -> code=42 msg="refused"
errors.Join     -> code=0  msg="refused\na second failure"
```

Through a real runner that returns `errors.Join(&jobs.Error{Code: 7}, cleanupErr)`, the job records `error_code=0`.

A runner that does *call the API, then clean up, and report both failures* is the natural way to write one, so this is
reachable in normal use.

**Fix**: `errors.As(err, &coded)`. Thirty lines of hand-rolled unwrapping replaced by the standard library, and the
`Unwrap() []error` case comes for free.

---

### 4. Dead code, most of it untested — *should fix*

`EncodePNG` and `AllJobStatuses` have **no caller and no test**. They will not survive a refactor, because nothing tells
their author what they were for.

| Symbol | Location | References |
| --- | --- | --- |
| `EncodePNG` | `internal/jobs/media.go:97` | none |
| `AllJobStatuses` | `internal/pocketbase/collections.go:32` | none |
| `SetRunner` | `internal/jobs/manager.go:156` | none |
| `MimeTypeOfName` | `internal/jobs/media.go:57` | its own test only |
| `ArtifactBytes` | `internal/jobs/artifacts.go:95` | one test only |
| `console.WithEmitter` | `internal/console/console.go:81` | tests only |
| `console.WithEmitterFunc` | `internal/console/console.go:91` | tests only |

For a *template* some unused exported API is defensible as an offering. The test is whether it is documented as such and
covered. These are neither: they read like leftovers.

**Fix**: delete them. An application that needs `EncodePNG` can write three lines.

---

### 5. Duplicated schema constants, justified by a comment that is false — *should fix*

`internal/jobs/collections.go:4`

> They are repeated here so that the jobs package does not have to import the persistence package, which would create a
> cycle.

There is no cycle. `internal/pocketbase` imports only its own `migrations` subpackage — nothing from `internal/jobs`. I
verified it by adding the import and compiling: it builds.

So the package duplicates eight constants, and the comment that justifies it is wrong. That is the worst combination: a
reader trusts the comment, and nobody ever tries the import.

**Fix**: delete `internal/jobs/collections.go`, import `pocketbase`, and let the schema own its own vocabulary.

---

### 6. `Submit` takes a context it ignores — *should fix*

`internal/jobs/manager.go:192`

The parameter is never read. Every caller — the bindings, the e2e check, every test — passes `context.Background()`. A
caller reading the signature would reasonably expect that cancelling it cancels the submission, or at least that it is used
for the database write. Neither is true.

**Fix**: either drop the parameter, or use it (`select` on `ctx.Done()` while waiting for a queue slot is the natural use).
Dropping is honest and cheap; the queue's cancellation already goes through `Cancel`.

---

### 7. `recordToJob` returns an error that cannot happen — *should fix*

`internal/jobs/manager.go:768`

The body has exactly one return, `return job, nil`. The `(Job, error)` signature is a promise it never keeps, and every
caller carries an error branch that cannot be taken — in `toJob`, in `toJobs`, and in `GetJob`.

**Fix**: return `Job`. The callers lose a branch each.

---

### 8. `AppInfo.Version` is never populated — *should fix*

`internal/bindings/service.go:204` builds `AppInfo{Name, HasAPIKey}` and never sets `Version`, which is documented as *"the
application version"*. `AppVersion` (line 230) is therefore unused, and the field always serialises as `""`.

**Fix**: set it, or delete both.

---

### 9. Three ways to set the emitter, and the one `main.go` uses is the odd one — *consider*

`WithEmitter`, `WithEmitterFunc` and `SetEmitter` all exist. `main.go:81` passes a **typed nil**:

```go
var bridge *bindings.EventBridge
jobManager := jobs.NewManager(pbService.App(), jobs.WithEmitter(bridge), ...)
// later
bridge = bindings.NewEventBridge(app)
jobManager.SetEmitter(bridge)
```

A nil pointer stored in an interface is not a nil interface, so `WithEmitter(bridge)` installs a non-nil emitter that
happens to be a nil pointer. It works only because `EventBridge.EmitJobUpdated` checks `b == nil` on its receiver. That is
two layers of defensive code protecting a call that should not be made.

The same three-way shape exists for the runner (`WithRunner`, `WithRunnerFunc`, `SetRunner`), where `SetRunner` is unused.

**Fix**: keep the option and drop the setter, and pass the emitter to the constructor once the app exists — the emitter
needs the app, so construct the app first, or keep `SetEmitter` alone and drop `WithEmitter`.

---

### 10. A render error blanks the window — *should fix*

There is no error boundary anywhere in `frontend/src`. In a browser a user reloads; in a desktop app there is no chrome to
reload from, so a thrown render blanks the window permanently.

**Fix**: one `ErrorBoundary` around `App`, showing the message and a retry.

---

### 11. Consecutive toasts cut each other short — *should fix*

`frontend/src/app/App.tsx:40`

```ts
setToast({ message, kind });
window.setTimeout(() => setToast(null), 4000);
```

The timer is never cleared. Notify at *t=0* and again at *t=3s* and the first timer fires at *t=4s*, hiding the second toast
after one second instead of four.

**Fix**: hold the timer id in a ref, clear it before setting a new one, and clear it on unmount.

---

### 12. `App.tsx` is a god component — *consider*

275 lines, 18 `useState`/`useEffect`/`useQuery`/`useMutation` calls, and it owns the layout, the data, the mutations, the
event wiring and the toast. The panels are nicely separated, but the shell that composes them is not.

**Fix**: extract the job mutations into `useJobs()`, the console into `useConsole()`, and the settings into `useSettings()`.
The shell then reads as a layout. Worth doing when the first real panel is added, not before.

---

### 13. Optional chaining that contradicts the type — *consider*

`internal/bindings/service.go:73` declares `UI pocketbase.UI` — a value, not a pointer, so it always serialises. The
frontend nonetheless guards it, and does so **inconsistently**:

- `App.tsx:160` — `ui.zoom`
- `App.tsx:170` — `settingsQuery.data.ui?.zoom ?? 100`
- `SettingsDialog.tsx:51` — `settings.ui?.zoom ?? 100`

Two of the three disagree with the first and with the Go type. The `??` is then unreachable.

**Fix**: trust the contract — `ui.zoom` — or make `UI` a pointer if it can really be absent. Pick one.

---

### 14. The settings dialog keeps the same value twice — *consider*

`SettingsDialog.tsx:41,44` hold `zoom` and `savedZoom` and keep them in sync by hand, to restore the value on Cancel. This
is the state that drifts: any new code path that changes one and not the other leaves Cancel restoring a stale zoom.

**Fix**: derive it. Keep a single `zoom`, and read the saved value from `settings.ui.zoom` when cancelling.

---

### 15. Image handling inside a queue that claims to be generic — *consider*

`internal/jobs/media.go` imports `image`, `image/jpeg`, `image/gif`, `image/png` to decode dimensions. The `artifacts`
collection carries `width` and `height` columns. Both are image-specific, in a package whose headline claim is *"the queue
is generic; the runner is yours"*.

A queue that stores an audio file, a PDF or a JSON blob still runs `DimensionsOf`, still fails the decode, and still carries
two dead columns.

**Fix**, in order of boldness:
1. Leave it, and say in `docs/architecture.md` that images are the expected common case.
2. Move the dimensions into the `meta` map, so the schema stays domain-free.
3. Move `media.go` into the runner.

Option 2 is the honest one for a scaffold; option 1 is acceptable if documented. What is not acceptable is leaving the
mismatch unmentioned.

---

### 16. Test gaps

| Package | Coverage | What is missing |
| --- | --- | --- |
| `internal/jobs` | 67.0% | `copyArtifact`, the `withRunner`/`withEmitter` options, `DeleteArtifact` |
| `internal/pocketbase` | 69.0% | the settings error paths |
| `internal/bindings` | 38.6% | most of the surface; no test of the *method set* (see finding 1) |
| `internal/runner` | 33.3% | `echo.go` has no test at all, though it ships as the default |
| `main` | 23.7% | `run()` is untested; only `combineHandlers` and `dataDir` are |

The echo runner is the one shipped default that a new user meets first, and it is the least tested thing in the repository.

`reconcile_test.go:124` also defines `newRecord`, a one-line wrapper around `core.NewRecord`. It adds a layer and hides the
call. Inline it.

---

### 17. Documentation drift

`docs/audit.md:6` states the scope as *"4,168 lines of Go"*. It is 4,654.

Small, but it is the kind of number that is wrong forever after the first refactor. Either compute it or drop it.

---

## Deliberate trade-offs I would keep

A review that only lists complaints is not a review. These looked questionable and turned out to be right:

- **Cancellation state in memory, not the database.** It is a desktop app with one process; persisting the cancel map would
  add a failure mode (stale entries after a crash) to solve a problem that does not exist.
- **A real database per test.** Slow — the `jobs` suite takes 7 seconds under `-race` — and worth every second, because it
  is what caught the data loss.
- **Plain CSS with custom properties.** No build step, no runtime, and the zoom scales it because everything is in `rem`.
  A CSS framework here would be weight without benefit.
- **A 30-minute default timeout.** Opinionated, and the right opinion: the failure it prevents (a hung runner stopping the
  queue) is silent, and the failure it causes (a legitimate long job) is loud and documented.
- **No i18n, no UI kit, three runtime dependencies.** The scaffold's value is that it can be read in an afternoon.

---

## Suggested order

| Order | Finding | Why now |
| --- | --- | --- |
| 1 | **Context leak** (2) | A real leak, in the hottest path, that the linter cannot see |
| 2 | **`errors.As`** (3) | Three-line fix for a silently lost error code |
| 3 | **Delete the three accessors** (1) | Deletion; removes a lie from the generated bindings |
| 4 | **`Submit`'s context, `recordToJob`'s error, `AppInfo.Version`** (6, 7, 8) | API honesty; each is a small edit |
| 5 | **Dead code** (4), **the false comment** (5) | Deletion; frees the reader from checking |
| 6 | **Error boundary, toast timer** (10, 11) | The two frontend bugs a user can see |
| 7 | **`App.tsx` split, `ui?.`, `savedZoom`** (12, 13, 14) | Polish; do it with the first real panel |
| 8 | **Image handling** (15), **tests** (16), **docs** (17) | Decide, cover, correct |

Findings 1–8 are each under an hour and together remove the whole of the "unused API" smell. That is where I would start.
