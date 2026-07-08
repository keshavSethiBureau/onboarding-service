package workflow

import (
	"context"
	"log"
	"time"

	"onboarding-service/internal/observability"
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
	stepEvents   StepEventSink
	metrics      *observability.Metrics // nil-safe; set via WithMetrics
}

// NewActivities wires the activities with their dependencies. Step events go
// to the log-only sink until an analytics transport is chosen; override with
// WithStepEventSink.
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
		stepEvents:   LogStepEventSink{},
	}
}

// WithStepEventSink swaps the analytics sink (tests, future real transport).
func (a *Activities) WithStepEventSink(s StepEventSink) *Activities {
	a.stepEvents = s
	return a
}

// WithMetrics attaches the observability instruments. Wiring calls this; tests
// that don't care about metrics leave it nil (all emit sites are nil-safe).
func (a *Activities) WithMetrics(m *observability.Metrics) *Activities {
	a.metrics = m
	return a
}

// ActionInput is the UNIFORM argument to every catalog action, so the executor
// can dispatch by name without per-step branching. OrgID/DisplayName are the
// accumulated journey context the executor threads across steps.
type ActionInput struct {
	UserID      string `json:"userId"`
	OrgID       string `json:"orgId"`
	DisplayName string `json:"displayName"`
	TncAccepted string `json:"tncAccepted"`
	Email       string `json:"email"`
}

// ActionResult is the UNIFORM result. The executor merges any non-empty field
// back into the journey context (generic — not keyed to a specific step).
type ActionResult struct {
	OrgID string `json:"orgId"`
	Email string `json:"email"`
}

// PersistJourneyState upserts the denormalised journey read-model (LLD §5),
// idempotent by userId.
func (a *Activities) PersistJourneyState(ctx context.Context, journey dto.OnboardingJourney) error {
	return a.journeys.Upsert(ctx, adapters.ToRepoOnboardingJourney(&journey))
}

// EmitStepEvent forwards the analytics step-event to the configured sink (its
// own activity, its own retry — a sink failure is retried without re-running
// the step's action). It is also the single place the funnel + journey
// lifecycle counters are emitted: this runs off the deterministic workflow path,
// once per completed step, so it is where onboarding_step_transitions_total and
// onboarding_workflow_started/completed belong (the workflow only sets the flags).
func (a *Activities) EmitStepEvent(ctx context.Context, evt StepEvent) error {
	if a.metrics != nil {
		a.metrics.StepTransitions.WithLabelValues(evt.Step, "completed").Inc()
		if evt.WorkflowStarted {
			a.metrics.WorkflowStarted.Inc()
		}
		if evt.WorkflowCompleted {
			a.metrics.WorkflowCompleted.Inc()
		}
	}
	return a.stepEvents.Emit(ctx, evt)
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
	// Fetch the user's email from Auth0 (as the auth service does) so later steps
	// (Lago billing) can thread it. Best-effort: a lookup failure must not fail
	// org creation — Lago tolerates an empty email.
	email, err := a.orgCreator.UserEmail(ctx, in.UserID)
	if err != nil {
		log.Printf("CreateOrganisation: user email lookup failed for %s: %v", in.UserID, err)
	}
	return ActionResult{OrgID: orgID, Email: email}, nil
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
	fn func(ctx context.Context, in provisioning.ProvisionInput) (string, error),
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
		return ActionResult{}, nil // already provisioned — idempotent, no external call
	}

	// Single place provisioning is measured: one external call per resource.
	start := time.Now()
	resourceID, err := fn(ctx, provisioning.ProvisionInput{
		OrgID: in.OrgID, DisplayName: in.DisplayName, Email: in.Email,
	})
	status := observability.StatusSuccess
	if err != nil {
		status = observability.StatusError
	}
	if a.metrics != nil {
		a.metrics.ProvisioningTotal.WithLabelValues(resource, status).Inc()
		a.metrics.ProvisioningDuration.WithLabelValues(resource).Observe(time.Since(start).Seconds())
	}
	log := observability.Log(ctx).With("resource", resource, "orgId", in.OrgID)
	if err != nil {
		// Temporal will retry the activity; surface the scheduled retry.
		log.Warn("provisioning failed, retry scheduled", "error", err.Error())
		return ActionResult{}, err
	}
	log.Info("provisioning succeeded", "resourceId", resourceID)

	rec.Resources[resource] = resourceID
	return ActionResult{}, a.provisioning.Upsert(ctx, rec)
}
