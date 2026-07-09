# Onboarding Service — API Contract

HTTP API for the platform onboarding service. All request/response bodies are JSON
(`Content-Type: application/json`). Identity for user-facing endpoints always comes
from the Auth0 token, never the request body (LLD §2.6).

Source of truth: `internal/controller/*.go`, `internal/auth/*.go`, `pkg/view/*.go`,
route wiring in `internal/app/wiring.go`.

---

## Authentication schemes

There are three access tiers. Each route below is tagged with the tier it uses.

| Tier | Applied to | How it works |
|------|-----------|--------------|
| **Public** | `/health`, `/ready`, `/metrics`, `/v1/verticals` | No auth. |
| **Auth0 (user)** | `/v1/onboarding/*` | `auth.Middleware`. See below. |
| **Internal** | `/v1/internal/*` | `X-Internal-Auth-Token` shared secret. See below. |

### Auth0 (user) tier

Behaviour depends on `auth.enabled` config.

- **Enabled (staging/prod):** requires `Authorization: Bearer <JWT>`. The token is
  validated as RS256 against the configured JWKS, with issuer + audience checks. The
  `sub` claim becomes `userId` (required — missing `sub` → `401`); the `org_id` claim
  becomes `orgId` (optional).
- **Disabled (local dev):** no JWT. Identity is read from headers:
  - `X-User-Id` — **required** (missing → `401`).
  - `X-Org-Id` — optional.

Any failure in this tier returns `401 { "error": "<reason>" }` (e.g. `missing or
malformed bearer token`, `invalid token`, `token missing sub claim`, or
`missing X-User-Id header (auth disabled dev mode)`).

### Internal tier

Guards service-to-service calls (only the Auth Service calls it).

- When `internal.authToken` is configured: requests must send
  `X-Internal-Auth-Token: <token>`. Missing/mismatched → `401 { "error": "invalid
  internal auth token" }`.
- When empty (local dev): the guard is a no-op; network isolation is the boundary.

---

## Endpoints

### GET /health — liveness — *Public*

Returns `200` as long as the process is up. Never checks dependencies.

**Response `200`**
```json
{ "status": "ok" }
```

---

### GET /ready — readiness — *Public*

Returns `200` only when every dependency probe passes, otherwise `503`. Each probe is
bounded by a 2s timeout. Current probes: `mongo`, `verticals` (cache non-empty),
`temporal`.

**Response `200`**
```json
{ "status": "ready", "checks": { "mongo": "ok", "verticals": "ok", "temporal": "ok" } }
```

**Response `503`** (one or more dependencies down)
```json
{ "status": "unavailable", "checks": { "mongo": "ok", "verticals": "ok", "temporal": "down" } }
```

---

### GET /metrics — Prometheus exposition — *Public*

Prometheus text exposition of HTTP + Mongo pool metrics. Content-type is the
Prometheus format, not JSON. Excluded from the request-count/latency metrics itself.

---

### GET /v1/verticals — list verticals — *Public*

Returns the active verticals served from the in-memory Apollo-backed cache (never
touches Mongo).

**Response `200`**
```json
{
  "verticals": [
    {
      "name": "string",
      "description": "string",
      "tags": ["string"]
    }
  ]
}
```

`verticals` is always present; it may be an empty array.

---

### GET /v1/onboarding/state — journey entry point + current position — *Auth0 (user)*

The **single journey entry point**, called on signup and on every login. `userId` is
taken from the validated token (never the body). Behavior:

- If **no journey exists** for the user, it creates one — starts the workflow
  (WorkflowID = userId) and records `USER_SIGNED_UP`.
- If the token's `email_verified` claim is **true** and `EMAIL_VERIFIED` is not yet
  recorded, it signals `EMAIL_VERIFIED`.
- Always returns `{ current_step, status }` so the frontend can route the user.

Idempotent: create-if-absent is a no-op when the workflow exists, duplicate step
signals are no-ops, and a completed journey just returns state. The first-ever call
may legitimately record both `USER_SIGNED_UP` and `EMAIL_VERIFIED`. This service makes
**no calls to the Auth Service** — the JWT is validated locally (cached Auth0 JWKS:
signature, expiry, issuer, audience) by `auth.Middleware`.

**Response `200`**
```json
{
  "current_step": "USER_SIGNED_UP",
  "status": "in_progress"
}
```

- `current_step` — one of the step names in the catalog (see [Step names](#step-names)).
- `status` — `in_progress` or `completed`.

**Errors**
- `401` — unauthenticated (see Auth0 tier).
- `500 { "error": "failed to load onboarding state" }`.

---

### POST /v1/onboarding/steps/{step_name} — advance a user-input step — *Auth0 (user)*

The **single write path** for user-input steps: one generic, catalog-driven handler
serves every user-advanceable step. `userId` comes from the validated token, never the
body.

**Path**
- `{step_name}` — the step to advance (e.g. `ORGANISATION_CREATED`, `VERTICAL_SELECTED`,
  `ONBOARDING_COMPLETED`). Must be in the caller's pinned catalog version **and** be
  their current step.

**Request**
```json
{ "input": { "display_name": "Acme Inc", "tnc_accepted": "true" } }
```
- `input` — an object of step-scoped fields, **opaque** to the generic handler. What it
  must contain is defined by the step's registered validator (below). A step with no user
  input (e.g. `ONBOARDING_COMPLETED`) takes `{ "input": {} }` or an empty body.

**Behavior**
1. **Ordering guard (one place):** `409` if `{step_name}` is not in the pinned catalog or
   is not the current step. A step already completed is a no-op returning current state.
2. **Validator registry lookup:** if the step has a registered validator, validate
   `input` (`400` on failure); if it has none, skip.
3. **Signal** the workflow's channel for `{step_name}` with the validated input; the
   step's activity performs the side effect (e.g. `CreateOrganisation`).
4. **Idempotent:** re-submitting a completed step is a no-op (Temporal ignores a
   duplicate signal for a step already passed).

**Registered validators (current)**
- `ORGANISATION_CREATED` — `display_name` non-empty **and** `tnc_accepted` non-empty.
- `VERTICAL_SELECTED` — `vertical_name` must exist in the vertical cache.
- `ONBOARDING_COMPLETED` — none (pure acknowledge-and-advance).

**Response `200`** (same shape as `/state`)
```json
{ "current_step": "PROVISION_KONG", "status": "in_progress" }
```

**Errors**
- `400 { "error": "<validation message>" }` — `body.input` failed the step's validator.
- `409 { "error": "step not current" }` — out-of-order, or step not in the pinned catalog.
- `401` — unauthenticated.
- `500 { "error": "failed to advance step" }`.

---

### POST /v1/internal/onboarding/steps — record an onboarding step — *Internal*

Called only by the Auth Service. Starts the user's onboarding workflow (WorkflowID =
userId) on the first step and signals it thereafter — atomic and safe under concurrent
calls. Identity comes from the body here (trusted service-to-service call), not a
token. The Auth Service calls this with `EMAIL_VERIFIED`.

**Request**
```json
{
  "user_id": "auth0|123",
  "org_id": "org_abc",
  "step_name": "EMAIL_VERIFIED"
}
```
- `user_id` — **required**.
- `step_name` — **required**. See [Step names](#step-names).
- `org_id` — optional.

**Response `202`**
```json
{ "user_id": "auth0|123", "step_name": "EMAIL_VERIFIED", "run_id": "<temporal-run-id>" }
```

**Errors**
- `400 { "error": "invalid request body" }` — malformed JSON.
- `400 { "error": "user_id and step_name are required" }` — missing required fields.
- `401 { "error": "invalid internal auth token" }` — bad/missing internal token.
- `500 { "error": "failed to record step" }`.

---

## Reference

### Step names

Recorded on the journey read-model and surfaced in `current_step` (`internal/workflow/catalog.go`, catalog v1, ordered):

`USER_SIGNED_UP` → `EMAIL_VERIFIED` → `ORGANISATION_CREATED` → `PROVISION_KONG` →
`PROVISION_AWS` → `VERTICAL_SELECTED` → `ONBOARDING_COMPLETED` →
`PROVISION_SVIX` → `PROVISION_LAGO` → `RESOURCES_PROVISIONED`.

### Status values

`in_progress`, `completed` (`internal/service/dto/onboarding_journey.go`).

### Conventions

- All errors use the shape `{ "error": "<message>" }`.
- `202 Accepted` responses carry `run_id` (the Temporal workflow run id) so callers
  can correlate the async work; poll `GET /v1/onboarding/state` for the outcome.

---

## Retired endpoints

RETIRED(generic-steps): the two typed write endpoints below are retired in favour of
`POST /v1/onboarding/steps/{step_name}`. Their behaviour is unchanged — only the trigger
moved from a typed endpoint to advancing the corresponding catalog step, with the
payload/validation now living in a registered validator + the step activity. Kept for
reference; greppable by `RETIRED(generic-steps)`.

### ~~POST /v1/onboarding/organisation~~ — RETIRED(generic-steps)

Replaced by `POST /v1/onboarding/steps/ORGANISATION_CREATED` with
`{ "input": { "display_name": "...", "tnc_accepted": "..." } }`. Previously: started the
workflow if absent, created the org via the `CreateOrganisation` activity, recorded
`ORGANISATION_CREATED`; required non-empty `display_name` + `tnc_accepted`; returned `202`
with `run_id`.

### ~~POST /v1/onboarding/complete~~ — RETIRED(generic-steps)

Replaced by `POST /v1/onboarding/steps/ONBOARDING_COMPLETED` (no input). Previously:
signalled completion so the user proceeds to the homepage while Svix/Lago provisioning
runs independently; returned `202` with `run_id`.
