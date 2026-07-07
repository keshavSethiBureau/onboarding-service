# Onboarding Service — LLD (build reference)

> Concise design reference for development. Reflects all final decisions.
> Status: current. Authoritative over any older docx.

## 1. Purpose

Owns the entire onboarding journey from login onward. The frontend authenticates
directly with Auth0 via SDK (Universal Login) — we do NOT proxy interactive
login/signup through any backend. After Auth0 returns the JWT, the frontend calls
its existing `/me` on the Auth Service (unchanged — no /me logic migrates here).
There are two entry points into the journey:
- **At signup:** the frontend calls `POST /v1/onboarding/signup` on this service,
  which calls Auth's `/me` to obtain the fresh signup identity/claims and uses them
  to start the journey — so the journey starts atomically at signup (see note below).
- **At every later login:** the frontend calls `GET /v1/onboarding/state` with the
  JWT (no proxying of the login `/me`).
From there this service owns everything: journey start,
email-verification tracking (read from the JWT's `email_verified` claim), organisation
creation (calling Auth0's Management API), vertical selection, questionnaire display,
and all post-org provisioning (Svix, Lago, and other migrated setup).

The Authentication Service makes NO calls into this service. `/me` stays in Auth
untouched (no logic migrates). The only call in the other direction is a single,
signup-only call from this service to Auth's `/me`, used to start the journey.

Atomic-start note: true cross-service transactionality is impossible (two systems,
no shared commit), so "atomic" here means effectively-atomic-and-safely-retryable.
The signup entry does (call /me) + (start workflow) together; because workflow start
is idempotent (WorkflowID = userId) and the state endpoint is idempotent, a
half-failed signup can be retried with no duplicate journey or corrupted state. If
Auth is slow/down at signup, retry; worst case the frontend falls back to the normal
`GET /v1/onboarding/state` entry point on the next authenticated call.

Migration note: today the frontend calls the Authentication Service, which calls
Auth0 to create the organisation and then runs post-creation setup. After this
change the frontend calls this Go service for `/me` and org creation, which calls
Auth0 and runs all setup. Cutover is a single hard release (no dual-run).

In scope: org creation (via Auth0), onboarding orchestration + drop-off tracking,
vertical selection, questions-per-vertical mapping (display only), migration of
Svix + Lago + other post-org setup from Auth.
Out of scope: storing questionnaire answers, capability mapping, template
recommendations, analytics dashboards, workflow versioning. (All future scope.)

## 2. Key decisions (do not deviate without updating this file)

1. **Orchestration = Temporal.** The onboarding flow is a Temporal workflow.
   Each step (and each external call, e.g. provisioning) is a Temporal activity
   with its own retry policy. Temporal is the source of truth for *what happened*
   and for resuming an interrupted flow. Chosen because the flow now has multiple
   system-side calls and steps that can fail and must retry/resume — real
   orchestration, not just a status field.
2. **Journey doc is a denormalised read-model.** Mongo holds ONE
   `onboarding_journeys` document per user: `currentStep`, `status`,
   `verticalName`, and an embedded summary of completed steps. This is the fast
   read for "where is this user" and for resume. There is **no separate
   `onboarding_steps` collection** and **no `user_verticals` collection.**
3. **Analytics comes from emitted step-events, NOT from the journey doc or
   Temporal history.** When a step completes, emit a lightweight step-event to the
   analytics sink. Rationale: Temporal history is not queryable for funnel
   analytics and is pruned/archived; the denormalised journey doc is not a good
   aggregation source. Keep operational state and analytics separate.
4. **Step catalog is versioned.** Each journey is pinned to the catalog version it
   started under; new steps apply only to journeys started after the change.
   Exception: a compliance-mandatory step may be force-applied deliberately.
5. **Verticals + questions live in Apollo config (`configlib`) + per-instance
   in-memory cache**, not Mongo. Hot-reload updates the cache; no custom refresh
   endpoint. Read-only at runtime.
6. **Identity comes from the Auth0 token** (userId, orgId), never request bodies.
7. **Svix + Lago provisioning are Temporal activities at the end of the workflow.**
   They retry via Temporal's retry policy. A provisioning failure must NOT block
   the user reaching the homepage; the workflow records the failure and retries
   independently of the user's progress.

## 3. Statuses (closed sets)

- Journey status: `in_progress` | `completed`
- Step status (within the embedded summary): `in_progress` | `completed`

## 4. Steps (catalog, versioned)

Current catalog (v1), in order:

```
LOGGED_IN               // first GET /v1/onboarding/state with a valid JWT -> starts the workflow
EMAIL_VERIFIED          // recorded when /v1/onboarding/state sees email_verified=true in the JWT
                        // (frontend refreshes the token after the user clicks the link)
ORGANISATION_CREATED    // Go service calls Auth0 Management API to create the org
<MIGRATED_POST_ORG_STEPS>  // TBD: derived by reading the Auth Service's org-creation flow;
                           // the real post-creation setup steps that move into Go
VERTICAL_SELECTED
QUESTIONNAIRE_VIEWED    // questions shown; answers not stored
ONBOARDING_COMPLETED
RESOURCES_PROVISIONED   // Svix + Lago (and other migrated setup) activities done
```

The `<MIGRATED_POST_ORG_STEPS>` placeholder is filled by reading the actual
org-creation API/flow in the Authentication Service (do not guess these). Each real
post-org-creation action there becomes a Temporal activity + step here.

Steps are plain strings and map to workflow progress. Adding a step = new string +
bump catalog version; in-flight journeys keep their pinned version.

Drop-off = read `currentStep` on the journey read-model. On return, route the user
to the first step they have not completed.

## 5. Temporal design

- **Step execution = versioned catalog + generic executor.** The step sequence is
  DATA: a catalog mapping a version number to an ordered list of step definitions,
  each naming a step and the activity (action) to run. `OnboardingWorkflow` is a
  generic executor that reads `stepCatalog[journey.StepCatalogVersion]` and walks it —
  no hardcoded `if step == X` branches. Adding/editing/removing a step = a new catalog
  version (data), never an executor edit.
- **Catalog immutability (determinism rule):** once any journey runs on a catalog
  version, that version's contents are IMMUTABLE. A change means a NEW version. This
  keeps Temporal replay deterministic (the workflow takes the same path on replay) and
  leaves in-flight journeys undisturbed. New journeys start on the latest version;
  in-flight journeys keep their pinned `StepCatalogVersion`.
- **Determinism:** the workflow function must be deterministic — no `time.Now`, no
  random, no direct DB/HTTP, no map-iteration-order dependence. All side effects live
  in activities.
- **Granular activities (not one mega-activity):** one activity per side effect, so
  each step retries/recovers independently. A single activity doing everything would
  re-run already-succeeded work on any failure and discard Temporal's core value.
- **Workflow:** `OnboardingWorkflow`, one per user (WorkflowID = userId), started
  by the first `GET /v1/onboarding/state` call (LOGGED_IN). This endpoint is
  idempotent: it is called on every login forever, so start-if-absent must be a no-op
  when the workflow exists, and signalling an already-completed step must be a no-op.
- **Signals:** the Auth Service (and the frontend) advance the workflow by sending
  signals — e.g. `EmailVerified`, `OrganisationCreated`, `VerticalSelected`,
  `QuestionnaireViewed`, `Complete`. A step needing user input receives that input as
  the signal payload. Human-paced waits are just the workflow durably awaiting the
  next signal (it can park for days across restarts).
- **Activities (each with a retry policy):**
  - `CreateOrganisation` — calls Auth0 to create the org (idempotent by userId/request key).
  - `PersistJourneyState` — upsert the denormalised journey read-model in Mongo.
  - `EmitStepEvent` — emit the analytics step-event.
  - `<migrated post-org setup activities>` — one per real action read from the Auth
    Service's org-creation flow (TBD from code).
  - `ProvisionSvix` — create the Svix application (idempotent; keyed by orgId).
  - `ProvisionLago` — create the Lago customer (idempotent; keyed by orgId).
- **Resume:** no manual resume logic. On crash, Temporal replays the workflow from
  history — completed activities return from history without re-running, and execution
  comes to rest exactly where it was (e.g. parked awaiting the next signal). The Mongo
  read-model is kept current by `PersistJourneyState` and serves the resume-screen read.
- **Idempotency:** provisioning activities use orgId as an idempotency key so
  retries never double-provision (belt-and-suspenders with the unique orgId index).

## 6. Data models

### 6.1 Mongo (own DB)

```go
// collection: onboarding_journeys ; unique index (userId)
type OnboardingJourney struct {
    ID                 string
    UserID             string        // primary lookup ; also Temporal WorkflowID
    OrgID              string
    CurrentStep        string
    Status             string        // in_progress | completed
    VerticalName       string        // denormalised; set on vertical selection
    StepCatalogVersion int           // pins which catalog this journey follows
    Steps              []StepSummary // embedded summary (denormalised)
    StartedAt          time.Time
    CompletedAt        *time.Time
    UpdatedAt          time.Time
}

type StepSummary struct {
    StepName    string
    Status      string        // in_progress | completed
    CompletedAt *time.Time
}

// collection: provisioning_records ; unique index (orgId)
type ProvisioningRecord struct {
    OrgID             string
    SvixApplicationID string
    LagoCustomerID    string
    Status            string        // pending | completed | failed
    CreatedAt         time.Time
    UpdatedAt         time.Time
}

// collection: step_catalogs ; unique index (version) ; INSERT-ONLY
// One document per catalog version. A version's steps are immutable once created —
// never updated or deleted. A step change = inserting a NEW version.
type StepCatalog struct {
    Version   int        // monotonically increasing; assigned as max(version)+1
    Steps     []StepDef  // ordered list of steps for this version
    CreatedAt time.Time
}

type StepDef struct {
    Name   string  // step name recorded on the journey
    Action string  // which activity handler runs for this step
}
```

Note: no `onboarding_steps` collection and no `user_verticals` collection — step
detail is embedded on the journey (operational) and emitted as events (analytics);
vertical lives on the journey.

### 6.1a Step catalog storage, caching & versioning (Mongo-backed)

- **Source of truth = `step_catalogs` in Mongo; runtime reads = local cache.** At
  startup, ALL versions are preloaded into a per-instance in-memory cache and served
  cache-only through the same lookup interface the workflow executor uses (executor
  unchanged). Readiness fails if the cache did not load.
- **Insert-only / immutable:** a version's steps never change once created. This is
  what keeps Temporal replay deterministic — a given `StepCatalogVersion` always
  resolves to the same ordered steps forever. Application logic rejects any attempt to
  modify an existing version; the unique `version` index backs this.
- **Cache-miss fallback:** a version inserted after startup is loaded once from Mongo
  on first lookup and cached forever (safe because immutable). A cached version is
  never re-read.
- **Creating a version:** read `currentMax = max(version)` (NOT a document count —
  count can diverge from max and must never derive a version number); insert with
  `version = currentMax + 1`; if the unique index rejects it (concurrent creation),
  re-read max and retry (bounded). Never update/delete an existing version.
- **Latest for new journeys:** `LatestVersion() = max(version)` in the cache (never
  count). New journeys pin `StepCatalogVersion = LatestVersion()` at workflow start;
  the pin never changes for the journey's life. Because the cache is preloaded at
  startup, a version inserted at runtime becomes "latest" only after instance restart —
  intentional: a new version's steps need newly deployed activity handlers, so the
  deploy that ships the handlers is what activates the version.
- **Deploy-order safety:** at startup, after preloading, validate that every `action`
  referenced by every catalog version has a registered activity handler; fail readiness
  otherwise.

### 6.2 Apollo config + cache (not Mongo)

```go
type Vertical struct {
    Name        string      // e.g. "KYC" — stored on the journey
    Description string
    Tags        []string    // future use; not used in V1 logic
}

type VerticalQuestions struct {
    VerticalName string
    Questions    []Question
}

type Question struct {
    Key     string
    Label   string
    Type    string      // single_choice | multi_choice | text | number | boolean
    Options []string
}
```

## 7. APIs

### Public (`/v1`, Auth0 token required; identity from token)
```
POST /v1/onboarding/organisation  frontend calls this to create the org (Go calls Auth0);
                                   starts the workflow and records ORGANISATION_CREATED
POST /v1/onboarding/signup         SIGNUP ENTRY POINT. Calls Auth's /me for fresh
                                    claims, starts the workflow (LOGGED_IN; EMAIL_VERIFIED
                                    if already true), returns journey state. Idempotent.
GET  /v1/onboarding/state          LOGIN ENTRY POINT + resume. Starts the workflow if
                                    absent (LOGGED_IN); reads email_verified from the
                                    JWT and signals EMAIL_VERIFIED when true; returns
                                    { current_step, status }. Idempotent on every call.
GET  /v1/verticals                 list active verticals (from cache)
POST /v1/onboarding/vertical       body { vertical_name }; signals VerticalSelected
GET  /v1/onboarding/questionnaire  questions for the journey's vertical (from cache)
POST /v1/onboarding/complete       signals Complete to the workflow
```

There are NO internal endpoints for the Authentication Service — it does not call
this service, and /me stays in Auth unchanged. The journey starts and advances from
`/v1/onboarding/state` (token claims) and the public endpoints below.

The frontend's org-creation call moves from the Auth Service to
`POST /v1/onboarding/organisation` on the Go service. Public write endpoints
translate to Temporal signals/activities. Verticals/questions refresh via Apollo
hot-reload, not an endpoint.

## 8. Critical flows

### Returning user (resume)
```
Auth0 login (frontend <-> Auth0 SDK) -> JWT { sub=userId, org_id, email_verified }
  -> frontend calls /me on Auth (unchanged, existing logic)
  -> frontend ALSO calls GET /v1/onboarding/state here (same JWT)
  -> starts workflow if absent (LOGGED_IN); signals EMAIL_VERIFIED if claim true
  -> returns currentStep from the journey read-model
  -> frontend routes to that step
```
The read-model always exists by return time because the first state call starts the
workflow, whose PersistJourneyState activity writes the journey. Note the claim
staleness rule: the JWT issued at signup has email_verified=false; after the user
clicks the verification link the frontend refreshes the token, and the next /me
call advances the step.

### Provisioning (end of workflow)
```
Complete signal
  -> workflow marks journey completed (PersistJourneyState); user proceeds to homepage
  -> ProvisionSvix activity (retry policy, idempotent by orgId)
  -> ProvisionLago activity (retry policy, idempotent by orgId)
  -> success: ProvisioningRecord.status=completed ; RESOURCES_PROVISIONED
  -> failure: Temporal retries independently; user is NOT blocked
```
A feature flag on the Auth Service disables its old Svix/Lago provisioning once this
service is live.

## 9. Migration from the Authentication Service

Hard cutover in a single release (no dual-run, no proxy):

- Before: frontend -> Auth Service -> Auth0 (create org) -> Auth runs post-org setup.
- After:  frontend -> Go service `POST /v1/onboarding/organisation` -> Auth0 (create org)
          -> Go workflow runs all post-org setup (migrated) -> Svix/Lago -> complete.

Steps to migrate correctly:
1. **Read the Auth Service's org-creation API/flow** and enumerate every action it
   performs after the Auth0 org-creation call (this defines `<MIGRATED_POST_ORG_STEPS>`
   and the corresponding Temporal activities). Do not guess these — derive from code.
2. Reimplement each as a Temporal activity in the Go service (idempotent).
3. Move the frontend's org-creation call to the Go endpoint.
4. Disable the org-creation + post-org setup path in the Auth Service in the same release.

Hard-cutover risk to note: any user mid-flow at release time. Since org creation is a
single call and the workflow is keyed by userId, a retry after cutover simply starts
the Go workflow; ensure the Auth0 org-creation activity is idempotent so a user who
half-completed under Auth is not double-created.

## 10. Concurrency / integrity invariants (review carefully)

- **One workflow + one journey per user** — WorkflowID = userId; unique index on
  userId. Starting a workflow that already exists must be a no-op/signal, not a
  duplicate.
- **Org creation idempotency** — the Auth0 CreateOrganisation activity must be
  idempotent (keyed by userId/request key) so retries or a post-cutover replay never
  create two orgs.
- **Provisioning idempotency** — orgId idempotency key on Svix/Lago (and migrated
  setup) activities + unique orgId index; retries never double-provision.

### Observability & abandonment
- Metrics (Prometheus via metricx): funnel counter `onboarding_step_transitions_total{step,status}`,
  workflow started/completed, `onboarding_activity_executions_total{action,status}` +
  duration histogram, provisioning success/latency, RED per HTTP route. Low-cardinality
  labels only — step/action/route/status, NEVER userId/orgId as labels (they go in logs).
- Logs: structured, with userId/orgId/workflowId/step and the OTel trace id. Inside
  workflow code use Temporal's replay-aware logger only (never a direct logger, which
  breaks determinism); emit metrics from activities/interceptors, not workflow code.
- Abandonment: journey status stays `in_progress` for a dropped user (no `abandoned`
  status). "Abandoned" is derived by query (in_progress + stale UpdatedAt). Optionally
  the workflow can race a `workflow.NewTimer` against the next signal via a Selector to
  fire a reminder / mark stale after N days — optional, not V1-blocking.

## 11. Peripherals & infrastructure (bureau-commons-go)

### Wire at startup (foundation)
- `configloader` — boot/infra config: Mongo URI, Auth0, Temporal address, ports.
- `configlib` (Apollo) — verticals + questions; hot-reload into per-instance cache.
- `mongoclient` — datastore singleton; create indexes at startup
  (unique userId on onboarding_journeys; unique orgId on provisioning_records;
  unique version on step_catalogs).
- `temporalclient` — Temporal workflow client + worker registration.
- `telemetry` — OpenTelemetry global setup at boot.
- `metricx` — Prometheus façade + `/metrics`.
- Startup also: preload ALL step_catalogs versions into the local cache, and validate
  every catalog action has a registered activity handler.
- Health check — liveness (process up) + readiness (Mongo connected, Temporal
  reachable, vertical cache warm, step-catalog cache preloaded, all catalog actions
  have registered handlers).

### Wire with the provisioning activities
- `httpclient` — Svix + Lago external calls (TLS, retry, pool, OTel). Temporal also
  retries at the activity level; use httpclient retry for transient transport
  errors and Temporal's for activity-level failures.
- `docstore` — only if provisioning stores blob assets.

### Optional / later
- `redisclient`, `lock` — likely NOT needed: Temporal + idempotency keys + the
  unique orgId index already cover provisioning safety. Add only on a concrete need.
- `kafkaclient` / `eventclient` — for the analytics step-events sink if events go
  over Kafka, and for emitting onboarding domain events to other services. Wire when
  the analytics sink / consumers are defined.

## 12. Layering (mirrors dendrite-store; do not share structs across layers)

```
write: view.Request -> dto.X -> adapters.ToRepoX -> repo.XDoc -> Mongo
read:  Mongo -> repo.XDoc -> adapters.FromRepoX -> dto.X -> view.Response
```
- DAO: `internal/repo` · DTO: `internal/service/dto` · View: `pkg/view`
- Adapters: `internal/service/dto/adapters` · Logic: `internal/service/impl`
- Temporal workflow + activities: `internal/workflow`
- Controllers: `internal/controller` · Config/cache: `internal/config`
- Wiring: `internal/app` · Entrypoint: `cmd/server`

## 13. Future scope (not now, but schema-compatible)

- Store questionnaire answers (new collection keyed by userId).
- Capability mapping from answers.
- Template recommendations by vertical (templates in dendrite-store, one vertical
  per template).
- Onboarding domain events to other services (via eventclient/Kafka).
