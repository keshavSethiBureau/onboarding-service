# Onboarding Service

Go service that owns the onboarding journey from login onward. The frontend
authenticates directly with Auth0 (SDK / Universal Login — never proxied through a
backend); it then calls its existing /me on the Auth Service (unchanged — this
service NEVER calls /me and never proxies it). ONE journey entry point: the frontend
calls GET /v1/onboarding/state with the JWT; if no journey exists we create the
workflow and record USER_SIGNED_UP, else we return state. This service owns: journey
start (USER_SIGNED_UP), email-verification tracking
(from the JWT email_verified claim), organisation creation (Auth0 Management API),
vertical selection, questionnaire display, and all post-org provisioning (Svix,
Lago, and other migrated setup). The Authentication Service is NOT in the
onboarding path — no calls in either direction.

Org-creation migration: the frontend's org-creation call moves from the Auth Service
to `POST /v1/onboarding/organisation` on this service (which calls Auth0). Hard
cutover in one release. The post-org setup steps to migrate are derived by READING
the Auth Service's org-creation flow — do not guess them.

Full design: `agent_docs/onboarding-lld.md` — read it before implementing a feature.

## Tech stack
- Go 1.22+
- Gin (HTTP)
- Temporal — onboarding is orchestrated as a Temporal workflow (steps + provisioning
  are activities with retry policies). Via commons `temporalclient`.
- MongoDB via commons `mongoclient` — holds the denormalised journey read-model
- Verticals and questions-per-vertical: Apollo config (`configlib`) + per-instance
  in-memory cache (NOT in Mongo)
- Boot/infra config via commons `configloader` (`${VAR:default}` expansion)
- Observability: commons `telemetry` (OpenTelemetry) + `metricx` (Prometheus)
- Auth: Auth0-issued JWT; identity (userId, orgId) from the token, never the body

## Commons packages (bureau-commons-go) — use these, don't hand-roll
Wire at startup:  configloader, configlib (Apollo), mongoclient, temporalclient, telemetry, metricx
Wire with provisioning activities: httpclient (Svix/Lago), docstore (if blobs)
Optional/later:   redisclient, lock (likely NOT needed — Temporal + idempotency keys cover it),
                  kafkaclient/eventclient (analytics step-events sink / domain events)

## Commands
- Build:   `go build ./...`
- Test:    `go test ./...`
- Lint:    `golangci-lint run`
- Run:     `go run ./cmd/server`
- Tidy:    `go mod tidy`

## Definition of done (every change)
- `go build ./...` succeeds
- `go test ./...` passes
- `golangci-lint run` is clean
- OpenAPI spec updated if an endpoint/DTO changed (`docs/openapi.yaml`)
- New behaviour has table-driven tests

## Architecture — 3-layer separation (mirrors dendrite-store)
Do NOT reuse a struct across layers; convert with adapters.
- DAO (Mongo shape, bson+json tags):   `internal/repo/`
- Service/DTO (business logic, json):   `internal/service/dto/`
- View (HTTP request/response):         `pkg/view/`
- Adapters DAO<->DTO:                    `internal/service/dto/adapters/`
- Business logic / lifecycle:           `internal/service/impl/`
- Temporal workflow + activities:       `internal/workflow/`
- HTTP handlers + routes:               `internal/controller/`
- Config load + cache (verticals, Qs):  `internal/config/`
- Wiring (repos->services->controllers):`internal/app/`
- Entrypoint:                           `cmd/server/`

Data flow on write: view.Request -> dto.X -> adapters.ToRepoX -> repo.XDoc -> Mongo
Data flow on read:  Mongo -> repo.XDoc -> adapters.FromRepoX -> dto.X -> view.Response

## Domain terms (map to code)
- Journey = OnboardingJourney; one per user; primary lookup by `userId` (unique index).
  A DENORMALISED read-model holding `currentStep`, `status`, `verticalName`, and an
  embedded `Steps` summary. No separate steps or user_verticals collection.
- Onboarding is a Temporal workflow (WorkflowID = userId). Steps advance via signals;
  Mongo journey doc is kept current by a PersistJourneyState activity.
- Analytics: emit a step-event per completed step to the analytics sink — NOT from the
  journey doc and NOT from Temporal history (neither is a good funnel source).
- Journey status: only `in_progress` | `completed`.
- Step status:    only `in_progress` | `completed`.
- Vertical = Bureau product area (Fraud, Credit, KYC, Onboarding); stored by NAME, from Apollo config.
- Step catalog is VERSIONED: a journey is pinned to the catalog version it started under;
  new steps apply only to journeys started after the change.

## Collections (Mongo, own DB)
- `onboarding_journeys`   — unique index (userId); denormalised read-model with embedded Steps
- `provisioning_records`  — unique index (orgId); Svix + Lago status
(No onboarding_steps collection — step detail is embedded on the journey; analytics via emitted events.)
(The step catalog is IN-CODE, not a Mongo collection — see the hard rules below.)

## Hard rules
- Identity (userId, orgId) is read from the Auth0 token, never from request bodies.
- There are NO internal endpoints for the Auth Service — it never calls this service.
- Onboarding is a Temporal workflow (WorkflowID = userId), started by the first
  GET /v1/onboarding/state call for a user with no journey (records USER_SIGNED_UP).
  That endpoint is idempotent: called on every login forever; create-if-absent and
  already-completed-step signals are no-ops. It reads email_verified from the JWT and
  signals EMAIL_VERIFIED when true (frontend refreshes the token after verification).
- ZERO calls between this service and the Auth Service, either direction. /me stays in
  Auth untouched — never call it, proxy it, reimplement it, or migrate its logic here.
- Every /v1 route validates the Auth0 JWT LOCALLY (signature, expiry, issuer, audience
  via cached JWKS — use commons/standard middleware, don't hand-roll). Never delegate
  token validation to the Auth Service.
- User-input steps advance via `POST /v1/onboarding/steps/{step_name}` (generic,
  catalog-driven, payload in `body.input`). Only `GET /v1/onboarding/state` and the
  internal steps endpoint are separate user entry points.
- Adding a user-input step = catalog data + a step activity + (if it has input) a
  registered validator. NEVER a new controller.
- Per-step input validation lives in a validator registry (step name → validate
  function) that the generic endpoint looks up — not in per-step controllers and not
  scattered in activities. Steps with no user input need no validator.
- The generic endpoint validates the step is in the user's pinned catalog and is the
  current step (reject out-of-order, 409), and is idempotent (re-submitting a completed
  step returns state).
- Org creation is owned by THIS service: advancing `ORGANISATION_CREATED` via the
  generic step endpoint (`POST /v1/onboarding/steps/ORGANISATION_CREATED`, input
  `{display_name, tnc_accepted}`) runs the idempotent CreateOrganisation activity (calls
  Auth0), records ORGANISATION_CREATED, then runs the migrated post-org setup activities.
- RETIRED(generic-steps): the typed `POST /v1/onboarding/organisation` and
  `POST /v1/onboarding/complete` endpoints are retired — organisation creation and
  completion are now driven by advancing their catalog steps (ORGANISATION_CREATED,
  ONBOARDING_COMPLETED) via the generic endpoint. Only the trigger changed; catalog
  contents and end-of-workflow provisioning behaviour are unchanged.
- The migrated post-org setup steps/activities are derived by reading the Auth
  Service's org-creation flow — never invented.
- The Mongo journey doc is a read-model kept current by the PersistJourneyState activity —
  it is not the orchestration source of truth (Temporal is).
- Svix + Lago provisioning are Temporal activities at the very END, idempotent by orgId,
  with retry policies. A provisioning failure must NOT block the user reaching the homepage.
- Analytics step-events are emitted per step; never derive funnel analytics from the journey
  doc or Temporal history.
- Verticals/questions are read-only at runtime from the in-memory cache, sourced from Apollo
  (`configlib`) with hot-reload. No custom refresh endpoint.
- Every handler, activity, and Mongo call is traced (telemetry) and measured (metricx).
- Step catalog is IN-CODE (`internal/workflow/catalog.go`), loaded into an immutable
  per-instance cache at startup; reads are cache-only. NEVER modify a shipped version —
  a step change is a NEW version key + a deploy (its handlers must ship with it); a
  golden test pins each shipped version. Latest = max(version), NEVER a version count.
- The workflow is a GENERIC EXECUTOR: it reads the steps for the journey's pinned
  StepCatalogVersion and walks them, dispatching each step's action by name. No
  hardcoded `if step == X` branches. Keep granular activities (one per side effect) —
  never a single mega-activity (it would re-run succeeded work on any retry).
- Workflow code MUST be deterministic: no time.Now, random, direct DB/HTTP, or
  map-iteration-order dependence; use Temporal's replay-aware logger inside workflows,
  never a direct logger. Emit metrics from activities/interceptors, not workflow code.
- Metric labels are low-cardinality only (step, action, route, status) — NEVER userId
  or orgId as labels (those belong in logs).
- At startup, validate every catalog action has a registered activity handler; fail
  startup otherwise (a new catalog version's handlers must ship in the same deploy).

## Boundaries (do not touch)
- Do not add answer-storage for the questionnaire (out of scope; only question->vertical mapping).
- Do not add template recommendations (future scope).
- Do not put verticals or questions in Mongo.
- Do not add an onboarding_steps or user_verticals collection (embedded on the journey).
- Do not edit a shipped in-code catalog version; add a NEW version. Do not derive latest from a count.

## Style
- Return typed errors via a shared error helper; map to HTTP status in the controller layer.
- Repository methods take a context.Context and are interface-defined in `internal/repo`.
- Prefer small, table-driven tests next to the package they cover.
