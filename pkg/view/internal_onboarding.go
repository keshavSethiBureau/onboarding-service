package view

// InternalStepRequest is the body of POST /v1/internal/onboarding/steps, called
// only by the Auth Service. Identity comes from the body here (trusted
// service-to-service call), not an Auth0 token.
type InternalStepRequest struct {
	UserID   string `json:"user_id"`
	OrgID    string `json:"org_id"`
	StepName string `json:"step_name"`
}
