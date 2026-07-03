// Package config loads boot/infra configuration via commons configloader
// (LLD §9): the values a process needs to start, with ${VAR:default} env
// expansion. Verticals and questions (Apollo configlib + in-memory cache) are
// a separate concern wired later.
package config

import "github.com/Bureau-Inc/bureau-commons-go/commons/configloader"

// Config holds boot/infra settings. Only the fields used today are present;
// Mongo URI, Auth0, and timeouts are added here as those peripherals are wired.
type Config struct {
	Server struct {
		Port string `yaml:"port"`
	} `yaml:"server"`
	Environment  string             `yaml:"environment"`
	Telemetry    TelemetryConfig    `yaml:"telemetry"`
	Apollo       ApolloConfig       `yaml:"apollo"`
	Auth         AuthConfig         `yaml:"auth"`
	Internal     InternalConfig     `yaml:"internal"`
	Provisioning ProvisioningConfig `yaml:"provisioning"`
}

// ProvisioningConfig holds the post-org provisioning integrations. When the
// endpoints are empty (local dev) the service falls back to a no-op stub
// provisioner; when set it uses the real Svix/Lago/Kong (httpclient) and AWS
// (SDK) clients.
type ProvisioningConfig struct {
	Svix ExternalServiceConfig `yaml:"svix"`
	Lago ExternalServiceConfig `yaml:"lago"`
	Kong ExternalServiceConfig `yaml:"kong"`
	AWS  AWSProvisioningConfig `yaml:"aws"`
}

// ExternalServiceConfig is an HTTP integration's base URL + bearer token.
type ExternalServiceConfig struct {
	BaseURL string `yaml:"baseUrl"`
	Token   string `yaml:"token"`
}

// AWSProvisioningConfig configures the API Gateway usage-plan integration.
type AWSProvisioningConfig struct {
	Region      string `yaml:"region"`
	UsagePlanID string `yaml:"usagePlanId"`
}

// InternalConfig guards the internal-network endpoints. An empty AuthToken
// leaves them open (local dev); set it to require a shared secret header.
type InternalConfig struct {
	AuthToken string `yaml:"authToken"`
}

// AuthConfig holds Auth0 JWT validation settings. When Enabled is false the
// middleware runs in dev mode (identity from X-User-Id/X-Org-Id headers).
type AuthConfig struct {
	Enabled    bool                  `yaml:"enabled"`
	Issuer     string                `yaml:"issuer"`
	Audience   string                `yaml:"audience"`
	JWKSURL    string                `yaml:"jwksUrl"`
	Management Auth0ManagementConfig `yaml:"management"`
}

// Auth0ManagementConfig holds the M2M credentials and tenant object ids the
// CreateOrganisation activity uses to call the Auth0 Management API. Mirrors the
// auth service's AUTHENTICATION_SVC_* keys (audience, connections, owner role).
type Auth0ManagementConfig struct {
	Domain                       string `yaml:"domain"`
	ClientID                     string `yaml:"clientId"`
	ClientSecret                 string `yaml:"clientSecret"`
	Audience                     string `yaml:"audience"`
	UsernamePasswordConnectionID string `yaml:"usernamePasswordConnectionId"`
	SSOConnectionID              string `yaml:"ssoConnectionId"`
	OwnerRoleID                  string `yaml:"ownerRoleId"`
}

// TelemetryConfig holds OpenTelemetry settings consumed by commons telemetry.
type TelemetryConfig struct {
	ServiceName  string `yaml:"serviceName"`
	OTLPEndpoint string `yaml:"otlpEndpoint"`
}

// ApolloConfig holds the configlib (Apollo) client settings. An empty MetaAddr
// runs configlib in defaults-only mode (no Apollo server, no hot-reload).
type ApolloConfig struct {
	Enabled   bool   `yaml:"enabled"`
	AppID     string `yaml:"appId"`
	Cluster   string `yaml:"cluster"`
	Namespace string `yaml:"namespace"`
	MetaAddr  string `yaml:"metaAddr"`
}

// Load reads the YAML config at path, applying configloader's ${VAR:default}
// environment expansion before unmarshalling.
func Load(path string) (*Config, error) {
	var c Config
	if err := configloader.LoadYAML(path, &c); err != nil {
		return nil, err
	}
	if c.Server.Port == "" {
		c.Server.Port = "8080" // safety default if file/env yield an empty port
	}
	if c.Telemetry.ServiceName == "" {
		c.Telemetry.ServiceName = "onboarding-service"
	}
	if c.Telemetry.OTLPEndpoint == "" {
		c.Telemetry.OTLPEndpoint = "localhost:4318"
	}
	return &c, nil
}
