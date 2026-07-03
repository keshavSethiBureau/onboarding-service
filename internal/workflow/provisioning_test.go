package workflow

import (
	"context"
	"testing"
)

// TestProvisioningActivities_Idempotent proves each post-org activity is
// idempotent by orgId: running it twice performs the external call only once
// (guarded by the provisioning record) and records the resource id.
func TestProvisioningActivities_Idempotent(t *testing.T) {
	ctx := context.Background()

	in := ActionInput{UserID: "u1", OrgID: "org1"}
	tests := []struct {
		name     string
		resource string
		run      func(a *Activities) error
		calls    func(p *countingProvisioner) int
	}{
		{"kong", resourceKong, func(a *Activities) error { _, e := a.ProvisionKong(ctx, in); return e },
			func(p *countingProvisioner) int { return p.kong }},
		{"svix", resourceSvix, func(a *Activities) error { _, e := a.ProvisionSvix(ctx, in); return e },
			func(p *countingProvisioner) int { return p.svix }},
		{"lago", resourceLago, func(a *Activities) error { _, e := a.ProvisionLago(ctx, in); return e },
			func(p *countingProvisioner) int { return p.lago }},
		{"aws", resourceAWS, func(a *Activities) error { _, e := a.ProvisionAWS(ctx, in); return e },
			func(p *countingProvisioner) int { return p.aws }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provRepo := newFakeProvisioningRepo()
			prov := &countingProvisioner{}
			acts := NewActivities(&stubJourneyRepo{}, provRepo, &countingOrgCreator{}, prov)

			// Run twice — the second must be a no-op.
			if err := tt.run(acts); err != nil {
				t.Fatalf("first run: %v", err)
			}
			if err := tt.run(acts); err != nil {
				t.Fatalf("second run: %v", err)
			}

			if got := tt.calls(prov); got != 1 {
				t.Errorf("external provision calls = %d, want 1 (idempotent)", got)
			}
			rec, err := provRepo.GetByOrgID(ctx, "org1")
			if err != nil || rec == nil {
				t.Fatalf("provisioning record missing: rec=%v err=%v", rec, err)
			}
			if _, ok := rec.Resources[tt.resource]; !ok {
				t.Errorf("resource %q not recorded: %+v", tt.resource, rec.Resources)
			}
		})
	}
}
