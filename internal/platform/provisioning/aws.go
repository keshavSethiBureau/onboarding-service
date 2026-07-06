package provisioning

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"
)

// apiGatewayAPI is the subset of the AWS API Gateway client we use; a seam so the
// AWS provisioning can be faked in tests without real AWS credentials.
type apiGatewayAPI interface {
	CreateApiKey(ctx context.Context, in *apigateway.CreateApiKeyInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateApiKeyOutput, error)
	CreateUsagePlanKey(ctx context.Context, in *apigateway.CreateUsagePlanKeyInput, optFns ...func(*apigateway.Options)) (*apigateway.CreateUsagePlanKeyOutput, error)
}

// awsGateway creates an API key for an org and attaches it to the basic usage
// plan (mirrors the Auth Service's add_organisation_to_basic_usage_plan).
type awsGateway struct {
	client      apiGatewayAPI
	usagePlanID string
}

// newAWSGateway builds the real gateway from ambient AWS config for the region.
func newAWSGateway(ctx context.Context, region, usagePlanID string) (*awsGateway, error) {
	if region == "" {
		region = "ap-south-1" // auth service default
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &awsGateway{client: apigateway.NewFromConfig(cfg), usagePlanID: usagePlanID}, nil
}

// ensureAPIKey creates the API key (keyed by orgId, idempotent — a ConflictException
// means it already exists) and attaches it to the basic usage plan.
func (g *awsGateway) ensureAPIKey(ctx context.Context, orgID, orgName string) (string, error) {
	out, err := g.client.CreateApiKey(ctx, &apigateway.CreateApiKeyInput{
		Name:        aws.String(orgID),
		Description: aws.String("[Do not delete] api key for the organisation" + orgName),
		Enabled:     true,
		Value:       aws.String(orgID + "_key_svc_random"),
	})
	if err != nil {
		var conflict *agtypes.ConflictException
		if errors.As(err, &conflict) {
			// Already created on a previous attempt — idempotent success.
			return orgID, nil
		}
		return "", fmt.Errorf("create api key: %w", err)
	}
	keyID := aws.ToString(out.Id)

	if _, err := g.client.CreateUsagePlanKey(ctx, &apigateway.CreateUsagePlanKeyInput{
		UsagePlanId: aws.String(g.usagePlanID),
		KeyId:       aws.String(keyID),
		KeyType:     aws.String("API_KEY"),
	}); err != nil {
		var conflict *agtypes.ConflictException
		if errors.As(err, &conflict) {
			return keyID, nil
		}
		return "", fmt.Errorf("attach usage plan key: %w", err)
	}
	return keyID, nil
}
