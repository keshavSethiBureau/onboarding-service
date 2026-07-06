// Package provisioning holds the post-organisation setup integrations migrated
// from the Authentication Service (Kong, Svix, Lago via commons httpclient; AWS
// via the AWS SDK). See HTTPProvisioner for the production implementation.
package provisioning

import "context"

// ProvisionInput carries the org context each provisioner needs. It mirrors what
// the auth service passes: the org id (dedup key), the user-facing display name
// (Kong tags / Svix + Lago name), and the user email (Lago customer).
type ProvisionInput struct {
	OrgID       string
	DisplayName string
	Email       string
}

// Provisioner performs one post-org setup action each. Every method MUST be
// idempotent by orgId — Temporal retries and replays must never double-provision
// (LLD §10). Each returns the external resource id it created/looked up.
type Provisioner interface {
	// Kong creates the API-gateway consumer for the org (Auth: create_consumer).
	Kong(ctx context.Context, in ProvisionInput) (resourceID string, err error)
	// Svix registers the webhook application.
	Svix(ctx context.Context, in ProvisionInput) (resourceID string, err error)
	// Lago creates the billing customer + plans + subscriptions.
	Lago(ctx context.Context, in ProvisionInput) (resourceID string, err error)
	// AWS creates the API key and attaches the basic usage plan (Auth: apigateway).
	AWS(ctx context.Context, in ProvisionInput) (resourceID string, err error)
}
