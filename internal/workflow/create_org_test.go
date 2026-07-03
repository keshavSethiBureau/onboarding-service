package workflow

import (
	"context"
	"testing"

	"onboarding-service/internal/repo"
)

// TestCreateOrganisation_Idempotent proves a retry / replay never creates two
// orgs for one user: once the journey carries an OrgID, CreateOrganisation
// returns it WITHOUT calling Auth0 again.
func TestCreateOrganisation_Idempotent(t *testing.T) {
	ctx := context.Background()
	creator := &countingOrgCreator{}
	prov := &countingProvisioner{}

	// First call: no journey yet -> Auth0 org creation runs once.
	first := &stubJourneyRepo{doc: nil}
	acts := NewActivities(first, newFakeProvisioningRepo(), creator, prov)
	res, err := acts.CreateOrganisation(ctx, ActionInput{UserID: "u1", DisplayName: "Acme"})
	if err != nil {
		t.Fatalf("first CreateOrganisation: %v", err)
	}
	if res.OrgID != "org_u1" {
		t.Fatalf("orgID = %q, want org_u1", res.OrgID)
	}
	if creator.calls != 1 {
		t.Fatalf("Auth0 create calls = %d, want 1", creator.calls)
	}

	// Retry: journey now carries the OrgID -> no second Auth0 call, same org.
	second := &stubJourneyRepo{doc: &repo.OnboardingJourneyDoc{UserID: "u1", OrgID: "org_u1"}}
	acts2 := NewActivities(second, newFakeProvisioningRepo(), creator, prov)
	res2, err := acts2.CreateOrganisation(ctx, ActionInput{UserID: "u1", DisplayName: "Acme"})
	if err != nil {
		t.Fatalf("retry CreateOrganisation: %v", err)
	}
	if res2.OrgID != "org_u1" {
		t.Fatalf("retry orgID = %q, want org_u1", res2.OrgID)
	}
	if creator.calls != 1 {
		t.Fatalf("Auth0 create calls after retry = %d, want 1 (idempotent, no duplicate org)", creator.calls)
	}
}
