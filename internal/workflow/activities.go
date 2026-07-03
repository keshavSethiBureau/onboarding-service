package workflow

import (
	"context"
	"log"

	"onboarding-service/internal/platform/auth0"
	"onboarding-service/internal/platform/provisioning"
	"onboarding-service/internal/repo"
	"onboarding-service/internal/service/dto"
	"onboarding-service/internal/service/dto/adapters"
)

// Activities holds the dependencies shared by onboarding Temporal activities.
type Activities struct {
	journeys     repo.OnboardingJourneyRepo
	provisioning repo.ProvisioningRecordRepo
	orgCreator   auth0.OrgCreator
	provisioner  provisioning.Provisioner
}

// NewActivities wires the activities with their dependencies.
func NewActivities(
	journeys repo.OnboardingJourneyRepo,
	provisioningRepo repo.ProvisioningRecordRepo,
	orgCreator auth0.OrgCreator,
	provisioner provisioning.Provisioner,
) *Activities {
	return &Activities{
		journeys:     journeys,
		provisioning: provisioningRepo,
		orgCreator:   orgCreator,
		provisioner:  provisioner,
	}
}

// ActionInput is the UNIFORM argument to every catalog action, so the executor
// can dispatch by name without per-step branching. OrgID/DisplayName are the
// accumulated journey context the executor threads across steps.
type ActionInput struct {
	UserID      string `json:"userId"`
	OrgID       string `json:"orgId"`
	DisplayName string `json:"displayName"`
	TncAccepted string `json:"tncAccepted"`
}

// ActionResult is the UNIFORM result. The executor merges any non-empty field
// back into the journey context (generic — not keyed to a specific step).
type ActionResult struct {
	OrgID string `json:"orgId"`
}

// StepEvent is the analytics event emitted per step transition.
type StepEvent struct {
	UserID string `json:"userId"`
	Step   string `json:"step"`
}

// PersistJourneyState upserts the denormalised journey read-model (LLD §5),
// idempotent by userId.
func (a *Activities) PersistJourneyState(ctx context.Context, journey dto.OnboardingJourney) error {
	return a.journeys.Upsert(ctx, adapters.ToRepoOnboardingJourney(&journey))
}

// EmitStepEvent emits the analytics step-event (its own activity, its own retry).
func (a *Activities) EmitStepEvent(_ context.Context, evt StepEvent) error {
	log.Printf("analytics: step-event user=%s step=%s", evt.UserID, evt.Step)
	return nil
}

// CreateOrganisation creates (or returns the existing) Auth0 org for the user and
// returns its id for the executor to thread. Idempotent by userId: layered on
// the journey OrgID check, the OrgCreator's own idempotency, and a SetOrgID
// backstop so a crash-then-retry sees the id (LLD §10).
func (a *Activities) CreateOrganisation(ctx context.Context, in ActionInput) (ActionResult, error) {
	if existing, err := a.journeys.FindByUserID(ctx, in.UserID); err != nil {
		return ActionResult{}, err
	} else if existing != nil && existing.OrgID != "" {
		return ActionResult{OrgID: existing.OrgID}, nil
	}
	orgID, err := a.orgCreator.CreateOrganisation(ctx, auth0.CreateOrgInput{
		UserID: in.UserID, DisplayName: in.DisplayName, TncAccepted: in.TncAccepted,
	})
	if err != nil {
		return ActionResult{}, err
	}
	if err := a.journeys.SetOrgID(ctx, in.UserID, orgID); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{OrgID: orgID}, nil
}

// ProvisionKong creates the API-gateway consumer for the org.
func (a *Activities) ProvisionKong(ctx context.Context, in ActionInput) (ActionResult, error) {
	return a.provision(ctx, in, resourceKong, a.provisioner.Kong)
}

// ProvisionAWS creates the API key + basic usage plan for the org.
func (a *Activities) ProvisionAWS(ctx context.Context, in ActionInput) (ActionResult, error) {
	return a.provision(ctx, in, resourceAWS, a.provisioner.AWS)
}

// ProvisionSvix registers the webhook app for the org.
func (a *Activities) ProvisionSvix(ctx context.Context, in ActionInput) (ActionResult, error) {
	return a.provision(ctx, in, resourceSvix, a.provisioner.Svix)
}

// ProvisionLago creates the billing customer for the org.
func (a *Activities) ProvisionLago(ctx context.Context, in ActionInput) (ActionResult, error) {
	return a.provision(ctx, in, resourceLago, a.provisioner.Lago)
}

// CompleteProvisioning marks the org's provisioning record completed once the
// end-of-onboarding activities have succeeded. Idempotent.
func (a *Activities) CompleteProvisioning(ctx context.Context, in ActionInput) (ActionResult, error) {
	rec, err := a.provisioning.GetByOrgID(ctx, in.OrgID)
	if err != nil {
		return ActionResult{}, err
	}
	if rec == nil {
		rec = &repo.ProvisioningRecordDoc{OrgID: in.OrgID, Resources: map[string]string{}}
	}
	rec.Status = repo.ProvisioningStatusCompleted
	return ActionResult{}, a.provisioning.Upsert(ctx, rec)
}

// Provisioning action names (also the keys in ProvisioningRecordDoc.Resources).
const (
	resourceKong = "kong"
	resourceSvix = "svix"
	resourceLago = "lago"
	resourceAWS  = "aws"
)

// provision runs one provisioning action idempotently by orgId: if the record
// already has this resource it is a no-op; otherwise the external call runs and
// the resource id is recorded.
func (a *Activities) provision(
	ctx context.Context,
	in ActionInput,
	resource string,
	fn func(ctx context.Context, orgID, orgName string) (string, error),
) (ActionResult, error) {
	rec, err := a.provisioning.GetByOrgID(ctx, in.OrgID)
	if err != nil {
		return ActionResult{}, err
	}
	if rec == nil {
		rec = &repo.ProvisioningRecordDoc{OrgID: in.OrgID, Resources: map[string]string{}}
	}
	if rec.Resources == nil {
		rec.Resources = map[string]string{}
	}
	if _, done := rec.Resources[resource]; done {
		return ActionResult{}, nil // already provisioned — idempotent
	}
	resourceID, err := fn(ctx, in.OrgID, in.OrgID)
	if err != nil {
		return ActionResult{}, err
	}
	rec.Resources[resource] = resourceID
	return ActionResult{}, a.provisioning.Upsert(ctx, rec)
}
