// Package auth0 holds the Auth0 Management integration used to create
// organisations (see HTTPOrgCreator).
package auth0

import "context"

// CreateOrgInput carries the fields the organisation-creation flow needs. It
// mirrors the auth service's POST /create-organisation inputs (userId from the
// header, displayName + tncAccepted from the body).
type CreateOrgInput struct {
	UserID      string
	DisplayName string
	TncAccepted string // prod passes T&C acceptance through as a string
}

// OrgCreator creates the Auth0 organisation for a user and returns its id.
//
// The contract mirrors the auth service (overwatch-authentication-service):
//
//  1. One org per user — implementations first check whether the user already
//     belongs to an org and reject if so (prod's is_user_owns_org guard).
//  2. The org name is random (organisation_<uuid>); the user↔org membership is
//     the dedup key, not the name.
//  3. Creation is ATOMIC: the org, its enabled connections, the user's
//     membership and the owner role are all established, or the org is deleted
//     and an error returned (prod's delete-on-failure). This keeps a retrying
//     caller (e.g. a Temporal activity) safe — a failed attempt leaves no
//     partial org behind, so the next attempt starts clean.
//
// HTTPOrgCreator is the production implementation; tests use their own fakes.
type OrgCreator interface {
	CreateOrganisation(ctx context.Context, in CreateOrgInput) (orgID string, err error)
}
