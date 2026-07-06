package provisioning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	hc "github.com/Bureau-Inc/bureau-commons-go/httpclient"
	httpconfig "github.com/Bureau-Inc/bureau-commons-go/httpclient/config"
	"github.com/Bureau-Inc/bureau-commons-go/httpclient/dtos"
	httperr "github.com/Bureau-Inc/bureau-commons-go/httpclient/errors"
	"github.com/Bureau-Inc/bureau-commons-go/metricx"
)

// Service / API names registered on the httpclient.
const (
	svcSvix = "svix"
	svcLago = "lago"
	svcKong = "kong"

	apiCreateApp          = "createApp"
	apiCreateCustomer     = "createCustomer"
	apiCreatePlan         = "createPlan"
	apiCreateSubscription = "createSubscription"
	apiCreateConsumer     = "createConsumer"
)

// Lago provisioning defaults (mirror the auth service's create-lago-customer-plans).
const (
	lagoCurrency    = "INR"
	lagoLegalNumber = "POC"
	lagoCountry     = "IN"
	lagoInterval    = "monthly"
)

// lagoEnvironments are the three per-environment plan/subscription pairs the auth
// service creates for every customer (plan code prefix + subscription name).
var lagoEnvironments = []struct{ CodePrefix, SubName string }{
	{"d_c_plan_", "Dev"},
	{"s_c_plan_", "Stg"},
	{"p_c_plan_", "Prod"},
}

// Settings configures the real provisioner's HTTP integrations + AWS.
type Settings struct {
	SvixBaseURL, SvixToken string
	LagoBaseURL, LagoToken string
	KongBaseURL, KongToken string
	Environment            string // tenancy tag on Kong consumers (Auth: ENVIRONMENT_KEY)
	AWSRegion              string
	AWSUsagePlanID         string
}

// HTTPProvisioner is the real Provisioner: Svix/Lago/Kong over commons httpclient
// and AWS over the AWS SDK. Each method is idempotent by orgId — a "already
// exists" (HTTP 409 / AWS ConflictException) is treated as success.
type HTTPProvisioner struct {
	http *hc.BureauHttpClient
	cfg  Settings
	aws  interface {
		ensureAPIKey(ctx context.Context, orgID, orgName string) (string, error)
	}
}

// NewHTTPProvisioner builds the real provisioner. The httpclient is configured
// in-code from the service base URLs; the AWS gateway is created from ambient
// AWS config for the region.
func NewHTTPProvisioner(ctx context.Context, s Settings, registry *metricx.Registry) (*HTTPProvisioner, error) {
	client, err := buildHTTPClient(s, registry)
	if err != nil {
		return nil, err
	}
	gw, err := newAWSGateway(ctx, s.AWSRegion, s.AWSUsagePlanID)
	if err != nil {
		return nil, err
	}
	return &HTTPProvisioner{http: client, cfg: s, aws: gw}, nil
}

// buildHTTPClient constructs the commons httpclient with the Svix/Lago/Kong
// services configured in-code from the base URLs.
func buildHTTPClient(s Settings, registry *metricx.Registry) (*hc.BureauHttpClient, error) {
	enableOTel := false
	cfg := &httpconfig.HttpConfiguration{
		EnableOpenTelemetry: &enableOTel,
		ServiceConfigs: map[string]httpconfig.ServiceConfig{
			svcSvix: {BaseURL: s.SvixBaseURL, ApiConfigs: map[string]httpconfig.ApiConfig{
				apiCreateApp: {Path: "/api/v1/app/", Method: "POST"},
			}},
			svcLago: {BaseURL: s.LagoBaseURL, ApiConfigs: map[string]httpconfig.ApiConfig{
				apiCreateCustomer:     {Path: "/api/v1/customers", Method: "POST"},
				apiCreatePlan:         {Path: "/api/v1/plans", Method: "POST"},
				apiCreateSubscription: {Path: "/api/v1/subscriptions", Method: "POST"},
			}},
			svcKong: {BaseURL: s.KongBaseURL, ApiConfigs: map[string]httpconfig.ApiConfig{
				apiCreateConsumer: {Path: "/consumers", Method: "POST"},
			}},
		},
	}
	client, err := hc.NewBureauHttpClient(cfg, registry)
	if err != nil {
		return nil, fmt.Errorf("build http client: %w", err)
	}
	return client, nil
}

// Kong creates the API-gateway consumer (username = orgId), tagged with the
// display name + environment, authenticated with the Kong admin token.
func (p *HTTPProvisioner) Kong(ctx context.Context, in ProvisionInput) (string, error) {
	body := map[string]any{
		"username":  in.OrgID,
		"custom_id": in.OrgID,
		"tags":      []string{in.DisplayName, p.cfg.Environment},
	}
	headers := map[string]string{"Authorization": "Bearer " + p.cfg.KongToken}
	if err := p.post(ctx, svcKong, apiCreateConsumer, headers, body); err != nil {
		return "", err
	}
	return in.OrgID, nil
}

// Svix registers the webhook application (uid = orgId, name = display name).
func (p *HTTPProvisioner) Svix(ctx context.Context, in ProvisionInput) (string, error) {
	body := map[string]any{"uid": in.OrgID, "name": in.DisplayName, "rateLimit": 100}
	headers := map[string]string{"Authorization": "Bearer " + p.cfg.SvixToken}
	if err := p.post(ctx, svcSvix, apiCreateApp, headers, body); err != nil {
		return "", err
	}
	return in.OrgID, nil
}

// Lago provisions billing exactly as the auth service does: a fully-configured
// customer (Stripe billing), three per-environment plans, and three
// subscriptions. Each call is idempotent (409 => already exists).
func (p *HTTPProvisioner) Lago(ctx context.Context, in ProvisionInput) (string, error) {
	headers := map[string]string{"Authorization": "Bearer " + p.cfg.LagoToken}

	// 1. Customer.
	customer := map[string]any{
		"external_id":  in.OrgID,
		"name":         in.DisplayName,
		"legal_name":   in.DisplayName,
		"legal_number": lagoLegalNumber,
		"currency":     lagoCurrency,
		"email":        in.Email,
		"country":      lagoCountry,
		"metadata":     []map[string]any{{"key": "type", "value": "automated_billing"}},
		"billing_configuration": map[string]any{
			"invoice_grace_period":     0,
			"payment_provider":         "stripe",
			"sync":                     true,
			"sync_with_provider":       true,
			"provider_payment_methods": []string{"card"},
		},
	}
	if err := p.post(ctx, svcLago, apiCreateCustomer, headers, map[string]any{"customer": customer}); err != nil {
		return "", err
	}

	// 2. Three plans (one per environment).
	for _, env := range lagoEnvironments {
		code := env.CodePrefix + in.OrgID
		plan := map[string]any{
			"name":            in.DisplayName,
			"code":            code,
			"amount_cents":    0,
			"amount_currency": lagoCurrency,
			"trial_period":    0,
			"interval":        lagoInterval,
			"pay_in_advance":  false,
			"description":     "Plan for " + in.DisplayName,
		}
		if err := p.post(ctx, svcLago, apiCreatePlan, headers, map[string]any{"plan": plan}); err != nil {
			return "", err
		}
	}

	// 3. Three subscriptions (anniversary billing from the 2nd of this month).
	subAt := secondOfMonthUTC()
	for _, env := range lagoEnvironments {
		sub := map[string]any{
			"external_customer_id": in.OrgID,
			"plan_code":            env.CodePrefix + in.OrgID,
			"external_id":          uuid.NewString(),
			"name":                 env.SubName,
			"subscription_date":    subAt,
			"billing_time":         "anniversary",
		}
		if err := p.post(ctx, svcLago, apiCreateSubscription, headers, map[string]any{"subscription": sub}); err != nil {
			return "", err
		}
	}

	return in.OrgID, nil
}

// secondOfMonthUTC returns the 2nd day of the current month at 00:00:00 UTC in
// RFC3339 (matches the auth service's subscription_date).
func secondOfMonthUTC() string {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), 2, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

// AWS creates the API key + attaches the basic usage plan via the AWS SDK.
func (p *HTTPProvisioner) AWS(ctx context.Context, in ProvisionInput) (string, error) {
	return p.aws.ensureAPIKey(ctx, in.OrgID, in.DisplayName)
}

// post executes a POST and treats HTTP 409 (already exists) as idempotent success.
func (p *HTTPProvisioner) post(ctx context.Context, service, api string, headers map[string]string, body any) error {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Content-Type"] = "application/json"
	req := &dtos.ApiRequest{ServiceName: service, ApiName: api, Headers: headers, RequestBody: body}

	var resp map[string]any
	err := p.http.ExecuteWithContext(ctx, req, &resp)
	if err == nil {
		return nil
	}
	var clientErr *httperr.ClientError
	if errors.As(err, &clientErr) && clientErr.StatusCode == 409 {
		return nil // already provisioned — idempotent
	}
	return err
}
