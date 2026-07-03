// Package auth0 holds the Auth0 Management integration used to create
// organisations (see HTTPOrgCreator).
package auth0

import "context"

// OrgCreator creates (or returns the existing) Auth0 organisation for a user.
//
// Implementations MUST be idempotent by userId — N calls (sequential retries OR
// concurrent) yield exactly one org and all return the same orgID (LLD §10).
// This is the load-bearing invariant: the caller's crash-before-persist retry
// window (activity creates the org, then dies before recording it) ultimately
// rests on this contract. A real Auth0 client MUST therefore:
//
//  1. Derive the dedup key from userId only — NEVER a random/timestamped value.
//  2. Tolerate a concurrent/prior create: use a stable, userId-derived org name
//     and on 409 "name already exists" GET that org and return its id
//     (Auth0's per-tenant name uniqueness becomes the dedup key).
//  3. Ignore displayName for dedup — a retry may carry a different display name;
//     the first org wins.
//
// HTTPOrgCreator is the production implementation; tests use their own fakes.
type OrgCreator interface {
	CreateOrganisation(ctx context.Context, userID, displayName string) (orgID string, err error)
}
