package provisioning

import (
	"context"
	"errors"
	"fmt"

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

	apiCreateApp      = "createApp"
	apiCreateCustomer = "createCustomer"
	apiCreateConsumer = "createConsumer"
)

// Settings configures the real provisioner's HTTP integrations + AWS.
type Settings struct {
	SvixBaseURL, SvixToken string
	LagoBaseURL, LagoToken string
	KongBaseURL            string
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
				apiCreateCustomer: {Path: "/api/v1/customers", Method: "POST"},
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

// Kong creates the API-gateway consumer (username = orgId).
func (p *HTTPProvisioner) Kong(ctx context.Context, orgID, orgName string) (string, error) {
	body := map[string]any{"username": orgID, "custom_id": orgID, "tags": []string{orgName}}
	if err := p.post(ctx, svcKong, apiCreateConsumer, nil, body); err != nil {
		return "", err
	}
	return orgID, nil
}

// Svix registers the webhook application (uid = orgId).
func (p *HTTPProvisioner) Svix(ctx context.Context, orgID, orgName string) (string, error) {
	body := map[string]any{"uid": orgID, "name": orgName, "rateLimit": 100}
	headers := map[string]string{"Authorization": "Bearer " + p.cfg.SvixToken}
	if err := p.post(ctx, svcSvix, apiCreateApp, headers, body); err != nil {
		return "", err
	}
	return orgID, nil
}

// Lago creates the billing customer (external_id = orgId).
func (p *HTTPProvisioner) Lago(ctx context.Context, orgID, orgName string) (string, error) {
	body := map[string]any{"customer": map[string]any{"external_id": orgID, "name": orgName}}
	headers := map[string]string{"Authorization": "Bearer " + p.cfg.LagoToken}
	if err := p.post(ctx, svcLago, apiCreateCustomer, headers, body); err != nil {
		return "", err
	}
	return orgID, nil
}

// AWS creates the API key + attaches the basic usage plan via the AWS SDK.
func (p *HTTPProvisioner) AWS(ctx context.Context, orgID, orgName string) (string, error) {
	return p.aws.ensureAPIKey(ctx, orgID, orgName)
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
