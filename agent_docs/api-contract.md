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

### GET /v1/onboarding/state — current onboarding position — *Auth0 (user)*

Returns where the authenticated user is in onboarding so the frontend can route them
on return. `userId` is taken from the token/dev headers.

**Response `200`**
```json
{
  "current_step": "EMAIL_VERIFIED",
  "status": "in_progress"
}
```

- `current_step` — one of the step names in the catalog (see [Step names](#step-names)).
- `status` — `in_progress` or `completed`.

**Errors**
- `401` — unauthenticated (see Auth0 tier).
- `500 { "error": "failed to load onboarding state" }`.

---

### POST /v1/onboarding/organisation — create organisation — *Auth0 (user)*

Triggers organisation creation for the authenticated user. Starts the onboarding
workflow if absent (WorkflowID = userId), calls Auth0 via the `CreateOrganisation`
activity, and records `ORGANISATION_CREATED`. Asynchronous: returns `202` immediately;
the resulting `orgId` later lands on the journey read-model (poll `GET
/v1/onboarding/state`).

**Request**
```json
{ "display_name": "Acme Inc" }
```
- `display_name` — **required**, non-empty. The `userId` comes from the token, never
  the body.

**Response `202`**
```json
{ "user_id": "auth0|123", "run_id": "<temporal-run-id>" }
```

**Errors**
- `400 { "error": "display_name is required" }` — missing/empty body or `display_name`.
- `401` — unauthenticated.
- `500 { "error": "failed to request organisation creation" }`.

---

### POST /v1/onboarding/complete — finish onboarding — *Auth0 (user)*

Signals the workflow to finish onboarding. Returns `202` immediately, before
end-of-onboarding provisioning (Svix + Lago) runs — the workflow marks the journey
completed so the user proceeds to the homepage, then provisions independently. A
provisioning failure never blocks this response. No request body.

**Request** — none.

**Response `202`**
```json
{ "user_id": "auth0|123", "run_id": "<temporal-run-id>" }
```

**Errors**
- `401` — unauthenticated.
- `500 { "error": "failed to complete onboarding" }`.

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

`EMAIL_VERIFIED` → `ORGANISATION_CREATED` → `PROVISION_KONG` → `PROVISION_AWS` →
`VERTICAL_SELECTED` → `QUESTIONNAIRE_VIEWED` → `ONBOARDING_COMPLETED` →
`PROVISION_SVIX` → `PROVISION_LAGO` → `RESOURCES_PROVISIONED`.

### Status values

`in_progress`, `completed` (`internal/service/dto/onboarding_journey.go`).

### Conventions

- All errors use the shape `{ "error": "<message>" }`.
- `202 Accepted` responses carry `run_id` (the Temporal workflow run id) so callers
  can correlate the async work; poll `GET /v1/onboarding/state` for the outcome.
