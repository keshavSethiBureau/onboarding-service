# Onboarding Service

Go service that owns everything from email verification onward: organisation
creation (by calling Auth0), onboarding orchestration, vertical selection,
questions-per-vertical mapping, and all post-org-creation provisioning (Svix, Lago,
and other setup migrated from the Auth Service). The Authentication Service keeps
signup, login, Auth0 login/token management, and email verification only.

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

## Hard rules
- Identity (userId, orgId) is read from the Auth0 token, never from request bodies.
- `/v1/internal/*` endpoints are callable only by the Auth Service (internal network); never public.
- Onboarding is a Temporal workflow (WorkflowID = userId). The Auth Service's ONLY
  call is the internal step signalling EMAIL_VERIFIED, which starts the workflow.
- Org creation is owned by THIS service: `POST /v1/onboarding/organisation` calls
  Auth0 to create the org (idempotent CreateOrganisation activity), records
  ORGANISATION_CREATED, then runs the migrated post-org setup activities.
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

## Boundaries (do not touch)
- Do not add answer-storage for the questionnaire (out of scope; only question->vertical mapping).
- Do not add template recommendations (future scope).
- Do not put verticals or questions in Mongo.
- Do not add an onboarding_steps or user_verticals collection (embedded on the journey).

## Style
- Return typed errors via a shared error helper; map to HTTP status in the controller layer.
- Repository methods take a context.Context and are interface-defined in `internal/repo`.
- Prefer small, table-driven tests next to the package they cover.
