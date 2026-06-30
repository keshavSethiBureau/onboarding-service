# Onboarding Service

Go service that owns everything between account creation and the home screen:
onboarding journey tracking, vertical selection, questions-per-vertical mapping,
and Svix + Lago provisioning. Authentication (signup, login, Auth0, email
verification) stays in the Authentication Service.

Full design: `agent_docs/onboarding-lld.md` — read it before implementing a feature.

## Tech stack
- Go 1.22+
- Gin (HTTP)
- MongoDB via commons `mongoclient` — primary datastore
- Verticals and questions-per-vertical: Apollo config (`configlib`) + per-instance
  in-memory cache (NOT in Mongo)
- Boot/infra config via commons `configloader` (`${VAR:default}` expansion)
- Observability: commons `telemetry` (OpenTelemetry) + `metricx` (Prometheus)
- Auth: Auth0-issued JWT; identity (userId, orgId) from the token, never the body

## Commons packages (bureau-commons-go) — use these, don't hand-roll
Wire at startup:  configloader, configlib (Apollo), mongoclient, telemetry, metricx
Wire at Step 6:   httpclient (Svix/Lago), lock (provisioning idempotency), redisclient, docstore
Do NOT use:       temporalclient (state is plain Go), kafkaclient/eventclient (V1 uses sync internal call)

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
- HTTP handlers + routes:               `internal/controller/`
- Config load + cache (verticals, Qs):  `internal/config/`
- Wiring (repos->services->controllers):`internal/app/`
- Entrypoint:                           `cmd/server/`

Data flow on write: view.Request -> dto.X -> adapters.ToRepoX -> repo.XDoc -> Mongo
Data flow on read:  Mongo -> repo.XDoc -> adapters.FromRepoX -> dto.X -> view.Response

## Domain terms (map to code)
- Journey  = OnboardingJourney; one per user; primary lookup is by `userId` (unique index).
  Holds `currentStep`, `status`, and the selected `verticalName` (denormalised onto journey).
- Step record = OnboardingStep; full per-step history for funnel analytics (separate collection).
- Journey status: only `in_progress` | `completed`.
- Step status:    only `in_progress` | `completed`.
- Vertical = Bureau product area (Fraud, Credit, KYC, Onboarding); stored by NAME, from config.
- Step catalog is VERSIONED: a journey is pinned to the catalog version it started under;
  new steps apply only to journeys started after the change.

## Collections (Mongo, own DB)
- `onboarding_journeys`   — unique index (userId); holds currentStep, status, verticalName
- `onboarding_steps`      — per-step history; index (journeyId)
- `provisioning_records`  — unique index (orgId); Svix + Lago status

## Hard rules
- Identity (userId, orgId) is read from the Auth0 token, never from request bodies.
- `/v1/internal/*` endpoints are callable only by the Auth Service (internal network); never public.
- The internal step endpoint UPSERTS the journey (creates it if absent) so early-step drop-off works.
- Svix + Lago provisioning runs at the VERY END (after journey marked completed), async; a
  provisioning failure must NOT block the user reaching the homepage. Record + retry instead.
- State management is plain Go code, not Temporal.
- Verticals/questions are read-only at runtime from the in-memory cache, sourced
  from Apollo (`configlib`) with hot-reload. No custom refresh endpoint.
- Every handler and Mongo call is traced (telemetry) and measured (metricx) from
  the start; add a funnel counter per onboarding step.

## Boundaries (do not touch)
- Do not add answer-storage for the questionnaire (out of scope; only question->vertical mapping).
- Do not add template recommendations (future scope).
- Do not put verticals or questions in Mongo.

## Style
- Return typed errors via a shared error helper; map to HTTP status in the controller layer.
- Repository methods take a context.Context and are interface-defined in `internal/repo`.
- Prefer small, table-driven tests next to the package they cover.
