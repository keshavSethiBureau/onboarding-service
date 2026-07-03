// Package provisioning holds the post-organisation setup integrations migrated
// from the Authentication Service (Kong, Svix, Lago via commons httpclient; AWS
// via the AWS SDK). See HTTPProvisioner for the production implementation.
package provisioning

import "context"

// Provisioner performs one post-org setup action each. Every method MUST be
// idempotent by orgId — Temporal retries and replays must never double-provision
// (LLD §10). Each returns the external resource id it created/looked up.
type Provisioner interface {
	// Kong creates the API-gateway consumer for the org (Auth: create_consumer).
	Kong(ctx context.Context, orgID, orgName string) (resourceID string, err error)
	// Svix registers the webhook application.
	Svix(ctx context.Context, orgID, orgName string) (resourceID string, err error)
	// Lago creates the billing customer/plan/subscription.
	Lago(ctx context.Context, orgID, orgName string) (resourceID string, err error)
	// AWS creates the API key and attaches the basic usage plan (Auth: apigateway).
	AWS(ctx context.Context, orgID, orgName string) (resourceID string, err error)
}
