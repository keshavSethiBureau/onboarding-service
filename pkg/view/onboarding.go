package view

// OnboardingStateResponse is the HTTP response for GET /v1/onboarding/state:
// where the user is in onboarding, for the frontend to route them on return.
type OnboardingStateResponse struct {
	CurrentStep string `json:"current_step"`
	Status      string `json:"status"`
}
