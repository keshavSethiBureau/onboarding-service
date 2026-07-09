package view

// RETIRED(generic-steps): CreateOrganisationRequest was the body of the retired
// POST /v1/onboarding/organisation. Org creation now advances ORGANISATION_CREATED
// via POST /v1/onboarding/steps/{step_name} with body { "input": { "display_name",
// "tnc_accepted" } }; the fields are validated by the ORGANISATION_CREATED entry in
// the validator registry. Retained commented per the removal convention.
//
// type CreateOrganisationRequest struct {
// 	DisplayName string `json:"display_name"`
// 	TncAccepted string `json:"tnc_accepted"`
// }
