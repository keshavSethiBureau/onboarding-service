package workflow

// Step execution is a versioned, data-driven catalog (LLD §5). The workflow is a
// generic executor that walks stepCatalog[journey.StepCatalogVersion] — adding or
// reordering a step is a new catalog version (data), never an executor edit.
//
// CRITICAL RULE: once a version is in use its contents are IMMUTABLE. A change is
// a NEW version key, never an edit to an existing list. This keeps Temporal
// replay deterministic and leaves in-flight journeys (pinned to their version)
// undisturbed.

// Step names recorded on the journey read-model.
const (
	StepEmailVerified        = "EMAIL_VERIFIED"
	StepOrganisationCreated  = "ORGANISATION_CREATED"
	StepProvisionKong        = "PROVISION_KONG"
	StepProvisionAWS         = "PROVISION_AWS"
	StepVerticalSelected     = "VERTICAL_SELECTED"
	StepQuestionnaireViewed  = "QUESTIONNAIRE_VIEWED"
	StepOnboardingCompleted  = "ONBOARDING_COMPLETED"
	StepProvisionSvix        = "PROVISION_SVIX"
	StepProvisionLago        = "PROVISION_LAGO"
	StepResourcesProvisioned = "RESOURCES_PROVISIONED"
)

// Action names — MUST match the activity method names registered on the worker,
// since the executor dispatches activities by this string.
const (
	ActionCreateOrganisation   = "CreateOrganisation"
	ActionProvisionKong        = "ProvisionKong"
	ActionProvisionAWS         = "ProvisionAWS"
	ActionProvisionSvix        = "ProvisionSvix"
	ActionProvisionLago        = "ProvisionLago"
	ActionCompleteProvisioning = "CompleteProvisioning"
)

// StepDef is one entry in the catalog: a step name, the activity to run for it
// (empty = record-only), the signal to await if it's user-driven (empty =
// system-driven, runs immediately), and whether reaching it marks the journey
// completed (so the user proceeds before end-of-flow provisioning).
type StepDef struct {
	Name          string
	Action        string
	Signal        string
	MarksComplete bool
}

// CatalogVersion is the version new journeys start on.
const CatalogVersion = 1

// stepCatalog maps a version to its ordered, IMMUTABLE step list.
var stepCatalog = map[int][]StepDef{
	// ---- Version 1 (IMMUTABLE once shipped) ----
	1: {
		{Name: StepEmailVerified, Signal: StepEmailVerified},
		{Name: StepOrganisationCreated, Signal: StepOrganisationCreated, Action: ActionCreateOrganisation},
		{Name: StepProvisionKong, Action: ActionProvisionKong},
		{Name: StepProvisionAWS, Action: ActionProvisionAWS},
		{Name: StepVerticalSelected, Signal: StepVerticalSelected},
		{Name: StepQuestionnaireViewed, Signal: StepQuestionnaireViewed},
		{Name: StepOnboardingCompleted, Signal: StepOnboardingCompleted, MarksComplete: true},
		{Name: StepProvisionSvix, Action: ActionProvisionSvix},
		{Name: StepProvisionLago, Action: ActionProvisionLago},
		{Name: StepResourcesProvisioned, Action: ActionCompleteProvisioning},
	},
}

// knownSteps is the set of valid step names across the built-in catalog. Used to
// bucket caller-supplied step values in metric labels so an arbitrary step_name
// on the internal endpoint can never blow up label cardinality.
var knownSteps = map[string]bool{
	StepEmailVerified: true, StepOrganisationCreated: true, StepProvisionKong: true,
	StepProvisionAWS: true, StepVerticalSelected: true, StepQuestionnaireViewed: true,
	StepOnboardingCompleted: true, StepProvisionSvix: true, StepProvisionLago: true,
	StepResourcesProvisioned: true,
}

// StepLabel returns name if it is a known step, else "unknown" — for safe,
// low-cardinality metric labelling of externally-supplied step names.
func StepLabel(name string) string {
	if knownSteps[name] {
		return name
	}
	return "unknown"
}

// CatalogSteps returns the ordered step list for a version (nil if unknown),
// read from the preloaded active catalog.
func CatalogSteps(version int) []StepDef { return activeCatalog.Steps(version) }

// LatestCatalogVersion is max(version) in the preloaded catalog. New journeys
// pin this at workflow start; the pin never changes for the journey's life.
func LatestCatalogVersion() int { return activeCatalog.LatestVersion() }

// FirstStep is the first step of the latest catalog version (resume-screen
// fallback). Falls back to the built-in version if the cache is somehow empty.
func FirstStep() string {
	steps := activeCatalog.Steps(activeCatalog.LatestVersion())
	if len(steps) == 0 {
		return stepCatalog[CatalogVersion][0].Name
	}
	return steps[0].Name
}
