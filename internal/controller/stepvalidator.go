package controller

import (
	"encoding/json"
	"fmt"
	"strings"

	"onboarding-service/internal/config"
	"onboarding-service/internal/workflow"
)

// StepValidator validates the opaque body.input for one user-input step. It
// receives the raw input bytes (may be empty) and returns a non-nil error to
// reject the request (surfaced as 400).
type StepValidator func(input json.RawMessage) error

// ValidatorRegistry maps a step name to its input validator. This is the SINGLE
// place per-step input validation lives — the generic step endpoint looks a
// validator up here; it is never in per-step controllers or scattered in
// activities. A step with no entry needs no input (validation is skipped).
type ValidatorRegistry map[string]StepValidator

// Validate runs the step's registered validator against input, or returns nil
// (skip) when the step has none.
func (r ValidatorRegistry) Validate(step string, input json.RawMessage) error {
	v, ok := r[step]
	if !ok {
		return nil
	}
	return v(input)
}

// NewValidatorRegistry builds the registry with the validators needed today.
// This is where the retired typed endpoints' validation now lives:
//   - ORGANISATION_CREATED: display_name + tnc_accepted non-empty (was POST /organisation).
//   - VERTICAL_SELECTED:    vertical_name must exist in the vertical cache.
func NewValidatorRegistry(verticals *config.VerticalCache) ValidatorRegistry {
	return ValidatorRegistry{
		workflow.StepOrganisationCreated: validateOrganisationCreated,
		workflow.StepVerticalSelected:    validateVerticalSelected(verticals),
	}
}

func validateOrganisationCreated(input json.RawMessage) error {
	var in struct {
		DisplayName string `json:"display_name"`
		TncAccepted string `json:"tnc_accepted"`
	}
	if err := decodeInput(input, &in); err != nil {
		return err
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		return fmt.Errorf("display_name is required")
	}
	if strings.TrimSpace(in.TncAccepted) == "" {
		return fmt.Errorf("tnc_accepted is required")
	}
	return nil
}

func validateVerticalSelected(verticals *config.VerticalCache) StepValidator {
	return func(input json.RawMessage) error {
		var in struct {
			VerticalName string `json:"vertical_name"`
		}
		if err := decodeInput(input, &in); err != nil {
			return err
		}
		if strings.TrimSpace(in.VerticalName) == "" {
			return fmt.Errorf("vertical_name is required")
		}
		if _, ok := verticals.Vertical(in.VerticalName); !ok {
			return fmt.Errorf("unknown vertical %q", in.VerticalName)
		}
		return nil
	}
}

// decodeInput unmarshals the (possibly empty) opaque input into v, treating an
// empty body as an empty object so required-field checks fail cleanly.
func decodeInput(input json.RawMessage, v any) error {
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	if err := json.Unmarshal(input, v); err != nil {
		return fmt.Errorf("invalid input")
	}
	return nil
}
