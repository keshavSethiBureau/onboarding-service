# Onboarding Service — LLD (build reference)

> Concise design reference for development. Reflects all final decisions.
> Status: current. Authoritative over any older docx.

## 1. Purpose

Owns everything from email verification onward: organisation creation (by calling
Auth0), onboarding orchestration, vertical selection, questions-per-vertical
display, and all post-org-creation resource provisioning (Svix, Lago, and other
setup migrated from the Authentication Service). The Authentication Service keeps
signup, login, Auth0 login/token management, and email verification only.

Migration note: today the frontend calls the Authentication Service, which calls
Auth0 to create the organisation and then runs post-creation setup. After this
change the frontend calls the **Go service**, which calls Auth0 to create the org
and runs all subsequent setup. Cutover is a single hard release (no dual-run).

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
EMAIL_VERIFIED          // signalled by Auth Service (its last responsibility) -> starts workflow
ORGANISATION_CREATED    // Go service calls Auth0 to create the org, then records this
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

- **Workflow:** `OnboardingWorkflow`, one per user (WorkflowID = userId), started
  when the first step signal arrives (EMAIL_VERIFIED) or on session create.
- **Signals:** the Auth Service (and the frontend) advance the workflow by sending
  signals — e.g. `EmailVerified`, `OrganisationCreated`, `VerticalSelected`,
  `QuestionnaireViewed`, `Complete`. Human-paced waits are just the workflow
  awaiting the next signal.
- **Activities (each with a retry policy):**
  - `CreateOrganisation` — calls Auth0 to create the org (idempotent by userId/request key).
  - `PersistJourneyState` — upsert the denormalised journey read-model in Mongo.
  - `EmitStepEvent` — emit the analytics step-event.
  - `<migrated post-org setup activities>` — one per real action read from the Auth
    Service's org-creation flow (TBD from code).
  - `ProvisionSvix` — create the Svix application (idempotent; keyed by orgId).
  - `ProvisionLago` — create the Lago customer (idempotent; keyed by orgId).
- **Resume:** if the process crashes, Temporal resumes the workflow from its last
  event; the Mongo read-model is kept current by `PersistJourneyState`.
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
```

Note: no `onboarding_steps` collection and no `user_verticals` collection — step
detail is embedded on the journey (operational) and emitted as events (analytics);
vertical lives on the journey.

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
GET  /v1/verticals                 list active verticals (from cache)
GET  /v1/onboarding/state          { current_step, status } from journey read-model
POST /v1/onboarding/vertical       body { vertical_name }; signals VerticalSelected
GET  /v1/onboarding/questionnaire  questions for the journey's vertical (from cache)
POST /v1/onboarding/complete       signals Complete to the workflow
```

### Internal (`/v1/internal`, Auth Service only, internal network)
```
POST /v1/internal/onboarding/steps  body { user_id, org_id, step_name }
                                     Auth's ONLY call: signals EMAIL_VERIFIED, starting
                                     the workflow. Everything after is owned by Go.
```

The frontend's org-creation call moves from the Auth Service to
`POST /v1/onboarding/organisation` on the Go service. Public write endpoints
translate to Temporal signals/activities. Verticals/questions refresh via Apollo
hot-reload, not an endpoint.

## 8. Critical flows

### Returning user (resume)
```
Auth0 login -> token { sub=userId, org_id }
  -> GET /v1/onboarding/state
  -> read journey read-model by userId -> return currentStep
  -> frontend routes to that step
```
The read-model always exists by return time because the first internal step starts
the workflow, whose PersistJourneyState activity writes the journey. If missing,
return the first step.

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

## 11. Peripherals & infrastructure (bureau-commons-go)

### Wire at startup (foundation)
- `configloader` — boot/infra config: Mongo URI, Auth0, Temporal address, ports.
- `configlib` (Apollo) — verticals + questions; hot-reload into per-instance cache.
- `mongoclient` — datastore singleton; create indexes at startup
  (unique userId on onboarding_journeys; unique orgId on provisioning_records).
- `temporalclient` — Temporal workflow client + worker registration.
- `telemetry` — OpenTelemetry global setup at boot.
- `metricx` — Prometheus façade + `/metrics`.
- Health check — liveness (process up) + readiness (Mongo connected, Temporal
  reachable, vertical cache warm).

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
