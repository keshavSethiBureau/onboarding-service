package workflow

// Step catalog (LLD §4), v1, in order. Steps are plain strings that map to
// workflow progress; adding a step means a new string + a catalog bump.
const (
	StepEmailVerified        = "EMAIL_VERIFIED"
	StepOrganisationCreated  = "ORGANISATION_CREATED"
	StepVerticalSelected     = "VERTICAL_SELECTED"
	StepQuestionnaireViewed  = "QUESTIONNAIRE_VIEWED"
	StepOnboardingCompleted  = "ONBOARDING_COMPLETED"
	StepResourcesProvisioned = "RESOURCES_PROVISIONED"
)

// CatalogVersion is the current step-catalog version journeys are pinned to.
const CatalogVersion = 1

// Catalog is the ordered v1 step list.
var Catalog = []string{
	StepEmailVerified,
	StepOrganisationCreated,
	StepVerticalSelected,
	StepQuestionnaireViewed,
	StepOnboardingCompleted,
	StepResourcesProvisioned,
}

// FirstStep is where a brand-new (or not-yet-created) journey starts.
func FirstStep() string { return Catalog[0] }

// TerminalStep is the last catalog step; reaching it ends the workflow. Note
// this is RESOURCES_PROVISIONED, which comes AFTER ONBOARDING_COMPLETED — the
// journey is marked completed at ONBOARDING_COMPLETED but the workflow stays
// alive to run provisioning, so late steps never spawn a second workflow.
var TerminalStep = Catalog[len(Catalog)-1]
