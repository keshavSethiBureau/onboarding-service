# How Temporal + Go Run the Onboarding Flow

This doc explains how the pieces in `internal/workflow/` fit together — the
workflow executor, the step catalog, activities, signals, and the worker — and
why the code is shaped the way it is. It maps each Temporal concept to the
plain-Go function you *wish* you could write.

---

## 1. The problem

Onboarding is a **days-long** process with humans in the middle:

```
 user verifies email ──► user names org ──► we provision infra ──► user picks
      (day 1)              (day 3?)          (Kong/AWS/...)         vertical ...
```

If you wrote it as ordinary Go, you'd want a straight-line function:

```go
// The program you WISH you could write:
func Onboard(userID string) {
    waitForEmailVerification()      // might take 2 days
    name := waitForOrgName()        // user closes laptop here
    org  := createAuth0Org(name)    // external call, might fail
    provisionKong(org)              // might get rate-limited
    provisionAWS(org)
    waitForVerticalSelection()
    // ...
}
```

A plain function like this dies the moment the pod restarts, and "wait 2 days"
can't live on a goroutine stack. The usual escape is to shatter it into a
state machine — a DB row with `current_step`, handlers that read the row,
decide what's next, write it back — plus hand-written retry and resume logic.

**Temporal lets you keep the straight-line function.** It makes the function
*durable*: it can crash mid-line and resume exactly where it was, on a
different machine, days later.

---

## 2. The trick: event history + replay

Temporal never snapshots a goroutine. Instead, the Temporal server keeps an
**append-only event history** per workflow execution:

```
 ┌──────────────────────────────────────────────────────────┐
 │  Event history for workflow "user_123"                   │
 ├──────────────────────────────────────────────────────────┤
 │  1. WorkflowStarted        {userId: user_123, ver: 1}    │
 │  2. SignalReceived         EMAIL_VERIFIED                 │
 │  3. ActivityScheduled      PersistJourneyState            │
 │  4. ActivityCompleted      → ok                           │
 │  5. SignalReceived         ORGANISATION_CREATED {Acme}    │
 │  6. ActivityScheduled      CreateOrganisation             │
 │  7. ActivityCompleted      → {orgId: org_9, email: a@b}   │
 │  8. ...                                                   │
 └──────────────────────────────────────────────────────────┘
```

When a worker needs to continue a workflow (a signal arrived, or the previous
worker crashed), it **re-runs the workflow function from the top**. Every
`workflow.ExecuteActivity(...)` call first checks the history:

```
                 replay of OnboardingWorkflow()
                 ──────────────────────────────
 ExecuteActivity(Persist...)   ── in history? YES ──► return recorded result
 Receive(EMAIL_VERIFIED)       ── in history? YES ──► return recorded payload
 ExecuteActivity(CreateOrg)    ── in history? YES ──► return recorded result
 ExecuteActivity(ProvisionKong)── in history? NO  ──► ACTUALLY RUN IT
                                                       (execution resumes here)
```

The function fast-forwards through everything it already did — in
microseconds, with **no side effects re-executed** — and comes to rest at the
first thing it hasn't done. That's why `OnboardingWorkflow` contains no manual
resume logic.

**The price: determinism.** Replay must take the same path every time. Hence:

- `workflow.Now(ctx)` instead of `time.Now()` (the time is recorded in history);
- `SignalPayload` is a typed struct, never a `map` (Go map iteration order is
  random — replay would diverge);
- all real I/O lives in activities, never in the workflow function itself.

---

## 3. The cast of characters

```
                 ┌─────────────────────────── our service ───────────────────────────┐
                 │                                                                    │
  user action    │  ┌──────────────┐      ┌─────────────┐                             │
 ──────────────► │  │ HTTP         │────► │  Starter    │──── SignalWithStart ───┐    │
  (REST call)    │  │ controller   │      │ (starter.go)│                        │    │
                 │  └──────────────┘      └─────────────┘                        ▼    │
                 │                                                    ┌────────────┐  │
                 │                                                    │  Temporal  │  │
                 │                                                    │  server    │  │
                 │                                                    │ (history + │  │
                 │                                                    │ task queue)│  │
                 │                                                    └─────┬──────┘  │
                 │  ┌──────────────────────────────┐        polls           │         │
                 │  │ Worker (wiring.go, Register) │◄────── "onboarding-────┘         │
                 │  │                              │         task-queue"              │
                 │  │  OnboardingWorkflow  ◄─── replayed here (deterministic)         │
                 │  │  Activities          ◄─── executed here (real I/O)              │
                 │  └───────────┬──────────────────┘                                  │
                 │              │                                                     │
                 └──────────────┼─────────────────────────────────────────────────────┘
                                ▼
                   Auth0 / Kong / AWS / Svix / Lago / MongoDB
```

| Piece | File | Plain-Go equivalent |
|---|---|---|
| `OnboardingWorkflow` | `onboarding_workflow.go` | the wish-list function, generalized into a loop |
| Step catalog | `catalog.go` | the script, **as data** (a `[]StepDef` per version) |
| `Activities` | `activities.go` | ordinary functions doing the real I/O |
| `ExecuteActivity` | (SDK) | "call fn with automatic retries, memoize the result forever" |
| Signal `Receive` | (SDK) | a durable `input()` / channel receive that survives restarts |
| `Starter` | `starter.go` | the thing that pokes the script from HTTP land |
| Worker | `wiring.go` → `Register` | the runtime that actually executes both |

---

## 4. The executor loop

`OnboardingWorkflow` is a **generic executor**: it walks the catalog and has
no per-step branching. Simplified skeleton:

```go
for _, step := range CatalogSteps(version) {
    saveCurrentStep(step.Name)          // 1. read-model: UI resume screen
    if step.Signal != "" {
        payload = waitFor(step.Signal)  // 2. durable "input()" — may block for days
    }
    if step.Action != "" {
        result = call(step.Action, ctx) // 3. durable, retried activity call
        ctx.merge(result)               //    (orgID, email flow forward)
    }
    markStepDone(step.Name)             // 4. journey history + completion flag
    emitAnalyticsEvent(step.Name)       // 5. fire-and-forget-ish analytics
}
```

Each step is one row of `StepDef`:

```go
type StepDef struct {
    Name          string // recorded on the journey read-model
    Action        string // activity METHOD NAME to run ("" = record-only)
    Signal        string // signal to await first    ("" = system-driven)
    MarksComplete bool   // reaching this = journey complete for the user
}
```

Catalog **version 1** and how a journey moves through it:

```
  v1 catalog                          driven by
  ─────────────────────────────────   ──────────────────────────
  EMAIL_VERIFIED          ⏸ signal    auth service (internal API)
  ORGANISATION_CREATED    ⏸ signal ▶ CreateOrganisation   user submits org name
  PROVISION_KONG                   ▶ ProvisionKong        system (immediate)
  PROVISION_AWS                    ▶ ProvisionAWS         system (immediate)
  VERTICAL_SELECTED       ⏸ signal                        user picks vertical
  QUESTIONNAIRE_VIEWED    ⏸ signal                        user views questionnaire
  ONBOARDING_COMPLETED    ⏸ signal   ★ MarksComplete      user finishes UI flow
  PROVISION_SVIX                   ▶ ProvisionSvix        system (immediate)
  PROVISION_LAGO                   ▶ ProvisionLago        system (immediate)
  RESOURCES_PROVISIONED            ▶ CompleteProvisioning system (immediate)

  ⏸ = workflow parks here until the signal arrives
  ▶ = activity dispatched BY NAME (string must equal the registered method name)
  ★ = user sees "done" here — Svix/Lago provisioning finishes in the background
```

Two rules keep this safe:

1. **Versions are immutable.** In-flight journeys are pinned to the version
   they started on and will *replay* against it. Changing the flow means
   adding `2: {...}` to the map — never editing `1:`.
2. **Action strings must match registered method names.** The executor
   dispatches activities by string; `Register` in
   `onboarding_workflow.go` is where the worker learns the mapping.

---

## 5. Signals: how HTTP reaches a sleeping workflow

```
 user clicks "Create org: Acme Inc"
        │
        ▼
 POST /v1/onboarding/...           (controller)
        │
        ▼
 Starter.SignalWithStartWorkflow(  workflowID = userId,
        │                          signal     = ORGANISATION_CREATED,
        │                          payload    = {displayName: "Acme Inc"} )
        ▼
 ┌─ Temporal server ────────────────────────────────────┐
 │  workflow for user_123 exists?                        │
 │    no  ──► start it, then deliver the signal          │  (atomic — no
 │    yes ──► just deliver the signal                    │   start/signal race)
 └───────────────────────────────────────────────────────┘
        │
        ▼
 workflow's  GetSignalChannel(ctx, "ORGANISATION_CREATED").Receive(...)
 unblocks with the payload; the loop continues.
```

While parked on a `Receive`, **no goroutine is blocked anywhere** — the
workflow is just rows in Temporal's DB. A pod restart while parked loses
nothing.

---

## 6. Activities: where the real world lives

Activities are plain methods (`CreateOrganisation`, `ProvisionKong`, ...) that
do HTTP calls and Mongo writes. The workflow wraps every call with a retry
policy (exponential backoff, capped attempts), and the *result* — not the
execution — is what history stores.

Because a retry can re-run an activity that half-succeeded, **every activity
is idempotent**:

```
 ProvisionKong attempt #1 ──► Kong: consumer created ──► pod dies before ack
 ProvisionKong attempt #2 ──► Kong: 409 Conflict     ──► treated as SUCCESS
                                                          (already provisioned)
```

The same pattern everywhere: Auth0/Svix/Lago/Kong return 409 → success; AWS
`ConflictException` → success; plus a `provisioning_records` Mongo guard.

Data flows *between* steps through the loop's local variables, merged from
each activity's `ActionResult`:

```
 CreateOrganisation ──► ActionResult{OrgID, Email}
                              │
              orgID, email ◄──┘   (workflow-local vars)
                              │
                              ▼
 ProvisionKong/AWS/Svix/Lago receive ActionInput{UserID, OrgID,
                                     DisplayName, TncAccepted, Email}
```

Replay-safe because the variables are reconstructed from recorded activity
results every time the function replays.

---

## 7. One concrete run, end to end

```
 day 1   user verifies email
         └► auth service ► internal endpoint ► SignalWithStart(EMAIL_VERIFIED)
            workflow starts, consumes the signal, persists journey,
            parks at ⏸ ORGANISATION_CREATED

         ═══ pod redeployed — nothing lost ═══

 day 3   user submits "Acme Inc"
         └► signal ORGANISATION_CREATED {displayName: "Acme Inc"}
            worker replays fn (history fast-forward, microseconds)
            ▶ CreateOrganisation: M2M token ► create org (connections)
              ► add member ► owner role ► fetch email
            ▶ ProvisionKong, ▶ ProvisionAWS  (immediate, retried)
            parks at ⏸ VERTICAL_SELECTED

 day 3   user picks vertical, views questionnaire, clicks done
         └► three signals; at ONBOARDING_COMPLETED ★ the journey is marked
            COMPLETED — the user moves on
            ▶ ProvisionSvix, ▶ ProvisionLago, ▶ CompleteProvisioning run
            in the background; final persist; workflow returns nil.
```

---

## 8. The mental model in one line

> The workflow function is a script whose every side-effect is memoized and
> whose every pause is durable; Temporal replays the script to "resume" it,
> and the catalog makes the script **data** instead of code.
