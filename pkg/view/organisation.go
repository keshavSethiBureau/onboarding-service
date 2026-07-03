package view

// CreateOrganisationRequest is the body of POST /v1/onboarding/organisation. The
// userId is taken from the Auth0 token, never the body (LLD §2.6).
type CreateOrganisationRequest struct {
	DisplayName string `json:"display_name"`
	TncAccepted string `json:"tnc_accepted"`
}
