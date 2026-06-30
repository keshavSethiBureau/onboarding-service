# Onboarding Service — LLD (build reference)

> Concise design reference for development. Reflects all final decisions.
> Status: current. Authoritative over any older docx.

## 1. Purpose

Owns everything between account creation and the home screen:
onboarding journey tracking, vertical selection, questions-per-vertical display,
and Svix + Lago provisioning. The Authentication Service keeps signup, login,
Auth0, token management, and email verification.

In scope: vertical selection, journey/drop-off tracking, questions-per-vertical
mapping (display only), Svix + Lago migration.
Out of scope: storing questionnaire answers, capability mapping, template
recommendations, analytics dashboards, workflow versioning. (All future scope.)

## 2. Key decisions (do not deviate without updating this file)

1. **State management = plain Go code**, not Temporal. State is a `currentStep`
   string plus step-history records. (Temporal only ever reconsidered for the
   provisioning tail, later — not now.)
2. **Step catalog is versioned.** Each journey is pinned to the catalog version
   it started under. New steps apply only to journeys started after the change.
   Exception: a compliance-mandatory step may be force-applied deliberately.
3. **Journey is the single per-user onboarding record.** The selected vertical is
   stored **on the journey** (`verticalName`). There is **no separate
   `user_verticals` collection.**
4. **Denormalise the summary onto the journey.** `currentStep` + `status` live on
   the journey doc (hot path = one read by `userId`). Full step history lives in a
   separate `onboarding_steps` collection (funnel analytics, read rarely).
5. **Verticals + questions live in Apollo config (`configlib`) + in-memory cache**,
   not Mongo. Hot-reload updates the cache; no custom refresh endpoint needed.
   Per-instance cache. They are near-static and read-only at runtime.
6. **Identity comes from the Auth0 token** (userId, orgId), never request bodies.
7. **Svix + Lago provisioning runs at the very end**, after the journey is marked
   completed, asynchronously. A provisioning failure must NOT block the homepage.

## 3. Statuses (closed sets)

- Journey status: `in_progress` | `completed`
- Step status:    `in_progress` | `completed`

## 4. Steps (catalog, versioned)

Current catalog (v1), in order:

```
EMAIL_VERIFIED          // recorded by Auth Service (internal API)
ORGANISATION_CREATED    // recorded by Auth Service (internal API)
VERTICAL_SELECTED
QUESTIONNAIRE_VIEWED    // questions shown; answers not stored
ONBOARDING_COMPLETED
RESOURCES_PROVISIONED   // Svix + Lago done (very end)
```

Steps are plain strings. Adding a step = new string + bump catalog version;
existing in-flight journeys keep their pinned version.

Drop-off = read `currentStep` on the journey. The first step a user has not
completed is where they are; route them there on return.

## 5. Data models

### 5.1 Stored in Mongo (own DB)

```go
// collection: onboarding_journeys ; unique index (userId)
type OnboardingJourney struct {
    ID                 string
    UserID             string      // primary lookup
    OrgID              string
    CurrentStep        string
    Status             string      // in_progress | completed
    VerticalName       string      // denormalised; set on vertical selection
    StepCatalogVersion int         // pins which catalog this journey follows
    StartedAt          time.Time
    CompletedAt        *time.Time
    UpdatedAt          time.Time
}

// collection: onboarding_steps ; index (journeyId)
type OnboardingStep struct {
    ID          string
    JourneyID   string
    StepName    string
    Status      string      // in_progress | completed
    CompletedAt *time.Time
    Metadata    map[string]interface{}
}

// collection: provisioning_records ; unique index (orgId)
type ProvisioningRecord struct {
    OrgID             string
    SvixApplicationID string
    LagoCustomerID    string
    Status            string      // pending | completed | failed
    CreatedAt         time.Time
    UpdatedAt         time.Time
}
```

### 5.2 Held in config + cache (not Mongo)

```go
type Vertical struct {
    Name        string      // e.g. "KYC" — this is what is stored on the journey
    Description string
    Tags        []string    // future use; not used in any V1 logic
}

type VerticalQuestions struct {
    VerticalName string
    Questions    []Question
}

type Question struct {
    Key     string      // stable id
    Label   string
    Type    string      // single_choice | multi_choice | text | number | boolean
    Options []string
}
```

Loaded on startup into an in-memory map keyed by vertical name; replaced
atomically on refresh.

## 6. APIs

### Public (`/v1`, Auth0 token required; identity from token)

```
GET  /v1/verticals                      list active verticals (from cache)
GET  /v1/onboarding/state               { current_step, status } for resume
POST /v1/onboarding/vertical            body { vertical_name }; sets verticalName on journey
GET  /v1/onboarding/questionnaire       questions for the journey's vertical (from cache)
POST /v1/onboarding/complete            mark completed; triggers provisioning (async)
```

### Internal (`/v1/internal`, Auth Service only, internal network)

```
POST /v1/internal/onboarding/steps      body { user_id, org_id, step_name }
                                         UPSERTS journey (create if absent), records step
```

Verticals/questions refresh is handled by Apollo hot-reload, not an endpoint.

## 7. Critical flows

### Returning user (resume)
```
Auth0 login -> token { sub=userId, org_id }
  -> GET /v1/onboarding/state
  -> lookup journey by userId -> return currentStep
  -> frontend routes to that step
```
Journey always exists by return time because the internal step API upserts it at
EMAIL_VERIFIED. If none found, return the first step.

### Provisioning (end of journey)
```
POST /v1/onboarding/complete
  -> journey.status = completed ; record ONBOARDING_COMPLETED
  -> async: create Svix app, create Lago customer
        success -> ProvisioningRecord.status=completed ; record RESOURCES_PROVISIONED
        failure -> ProvisioningRecord.status=failed ; schedule retry (does NOT fail complete)
  -> user proceeds to homepage regardless
```
Provisioning must be idempotent (do not double-provision an org). A feature flag on
the Auth Service disables its old Svix/Lago provisioning once this service is live.

## 8. Concurrency / integrity invariants (review these carefully)

- **One journey per user** — enforced by unique index on `userId`; the internal
  upsert must be create-if-absent without creating duplicates under concurrent calls.
- **Provisioning idempotency** — unique index on `orgId`; retries must not create a
  second Svix app / Lago customer.

## 9. Peripherals & infrastructure (bureau-commons-go)

Wire foundation peripherals before the service layer. Use commons packages; do
not hand-roll equivalents.

### Wire at startup (foundation)
- `configloader` — boot/infra config: Mongo URI, Auth0 settings, ports, timeouts
  (`${VAR:default}` env expansion).
- `configlib` (Apollo) — verticals + questions; hot-reload into per-instance
  in-memory cache. This replaces any custom refresh endpoint.
- `mongoclient` — datastore singleton; create the 3 collections' indexes at startup
  (unique userId; journeyId; unique orgId).
- `telemetry` — OpenTelemetry global setup at boot (so every handler + Mongo call
  is traced from day one).
- `metricx` — Prometheus façade + `/metrics`; add onboarding funnel counters
  (drop-off per step) as steps are built.
- Health check — liveness (process up) + readiness (Mongo connected AND vertical
  cache warm/non-empty).

### Wire at the provisioning step (Step 6, not before)
- `httpclient` — Svix + Lago external calls (config-driven TLS, retry, pool, OTel).
- `lock` — Redis-backed distributed lock guarding provisioning idempotency
  (belt-and-suspenders over the unique orgId index).
- `redisclient` — comes with `lock`; optional shared cache later.
- `docstore` — only if provisioning stores blob assets.

### Do NOT wire (V1)
- `temporalclient` — onboarding state is plain Go (see decision 1).
- `kafkaclient` / `eventclient` — V1 uses the synchronous internal Auth→Onboarding
  call; adopt events only when onboarding must emit domain events to other services.

## 10. Layering (mirrors dendrite-store; do not share structs across layers)

```
write: view.Request -> dto.X -> adapters.ToRepoX -> repo.XDoc -> Mongo
read:  Mongo -> repo.XDoc -> adapters.FromRepoX -> dto.X -> view.Response
```
- DAO: `internal/repo` · DTO: `internal/service/dto` · View: `pkg/view`
- Adapters: `internal/service/dto/adapters` · Logic: `internal/service/impl`
- Controllers: `internal/controller` · Config/cache: `internal/config`
- Wiring: `internal/app` · Entrypoint: `cmd/server`

## 11. Future scope (not now, but schema-compatible)

- Store questionnaire answers (new collection keyed by userId).
- Capability mapping from answers.
- Template recommendations by vertical (templates live in dendrite-store, one
  vertical per template).
