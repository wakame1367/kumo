package apigateway

import (
	"encoding/json"
	"time"

	"github.com/sivchari/kumo/internal/service"
)

// RestAPI represents an API Gateway REST API.
type RestAPI struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Description            string            `json:"description,omitempty"`
	CreatedDate            time.Time         `json:"createdDate"`
	Version                string            `json:"version,omitempty"`
	APIKeySource           string            `json:"apiKeySource,omitempty"`
	EndpointConfiguration  *EndpointConfig   `json:"endpointConfiguration,omitempty"`
	DisableExecuteAPIEndpt bool              `json:"disableExecuteApiEndpoint,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
	RootResourceID         string            `json:"-"` // Internal use.
	Policy                 string            `json:"policy,omitempty"`
	BinaryMediaTypes       []string          `json:"binaryMediaTypes,omitempty"`
	MinimumCompressionSize *int32            `json:"minimumCompressionSize,omitempty"`
}

// EndpointConfig represents the endpoint configuration for an API.
type EndpointConfig struct {
	Types          []string `json:"types,omitempty"`
	VpcEndpointIDs []string `json:"vpcEndpointIds,omitempty"`
	IPAddressType  string   `json:"ipAddressType,omitempty"`
}

// Resource represents an API Gateway resource.
type Resource struct {
	ID              string            `json:"id"`
	ParentID        string            `json:"parentId,omitempty"`
	PathPart        string            `json:"pathPart,omitempty"`
	Path            string            `json:"path"`
	ResourceMethods map[string]Method `json:"resourceMethods,omitempty"`
}

// Method represents an API Gateway method.
type Method struct {
	HTTPMethod          string                     `json:"httpMethod"`
	AuthorizationType   string                     `json:"authorizationType,omitempty"`
	AuthorizerID        string                     `json:"authorizerId,omitempty"`
	APIKeyRequired      bool                       `json:"apiKeyRequired,omitempty"`
	OperationName       string                     `json:"operationName,omitempty"`
	RequestParameters   map[string]bool            `json:"requestParameters,omitempty"`
	RequestModels       map[string]string          `json:"requestModels,omitempty"`
	RequestValidatorID  string                     `json:"requestValidatorId,omitempty"`
	AuthorizationScopes []string                   `json:"authorizationScopes,omitempty"`
	MethodResponses     map[string]*MethodResponse `json:"methodResponses,omitempty"`
	MethodIntegration   *Integration               `json:"methodIntegration,omitempty"`
}

// MethodResponse is a method response: a status code the method can return
// and the response headers / models it declares.
type MethodResponse struct {
	StatusCode         string            `json:"statusCode"`
	ResponseParameters map[string]bool   `json:"responseParameters,omitempty"`
	ResponseModels     map[string]string `json:"responseModels,omitempty"`
}

// IntegrationResponse maps an integration result, selected by
// SelectionPattern, to a method response status code.
type IntegrationResponse struct {
	StatusCode         string            `json:"statusCode"`
	SelectionPattern   string            `json:"selectionPattern,omitempty"`
	ResponseParameters map[string]string `json:"responseParameters,omitempty"`
	ResponseTemplates  map[string]string `json:"responseTemplates,omitempty"`
	ContentHandling    string            `json:"contentHandling,omitempty"`
}

// PutMethodResponseRequest is the body of PutMethodResponse.
type PutMethodResponseRequest struct {
	ResponseParameters map[string]bool   `json:"responseParameters,omitempty"`
	ResponseModels     map[string]string `json:"responseModels,omitempty"`
}

// PutIntegrationResponseRequest is the body of PutIntegrationResponse.
type PutIntegrationResponseRequest struct {
	SelectionPattern   string            `json:"selectionPattern,omitempty"`
	ResponseParameters map[string]string `json:"responseParameters,omitempty"`
	ResponseTemplates  map[string]string `json:"responseTemplates,omitempty"`
	ContentHandling    string            `json:"contentHandling,omitempty"`
}

// PatchOperation is one entry of an Update* request's patchOperations.
type PatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value,omitempty"`
	From  string `json:"from,omitempty"`
}

// UpdateRequest is the body shared by every Update* (PATCH) operation.
type UpdateRequest struct {
	PatchOperations []PatchOperation `json:"patchOperations"`
}

// Authorizer represents an API Gateway authorizer. Only the fields kumo needs
// to run a REQUEST-type Lambda authorizer are modeled.
type Authorizer struct {
	ID                           string `json:"id"`
	RestAPIID                    string `json:"-"` // internal: owning REST API
	Name                         string `json:"name"`
	Type                         string `json:"type"` // "REQUEST" | "TOKEN"
	AuthorizerURI                string `json:"authorizerUri,omitempty"`
	IdentitySource               string `json:"identitySource,omitempty"`
	AuthorizerResultTTLInSeconds int32  `json:"authorizerResultTtlInSeconds,omitempty"`
}

// Integration represents an API Gateway integration.
type Integration struct {
	Type                string            `json:"type"`
	HTTPMethod          string            `json:"httpMethod,omitempty"`
	URI                 string            `json:"uri,omitempty"`
	ConnectionType      string            `json:"connectionType,omitempty"`
	ConnectionID        string            `json:"connectionId,omitempty"`
	PassthroughBehavior string            `json:"passthroughBehavior,omitempty"`
	ContentHandling     string            `json:"contentHandling,omitempty"`
	TimeoutInMillis     int32             `json:"timeoutInMillis,omitempty"`
	CacheNamespace      string            `json:"cacheNamespace,omitempty"`
	CacheKeyParameters  []string          `json:"cacheKeyParameters,omitempty"`
	RequestParameters   map[string]string `json:"requestParameters,omitempty"`
	RequestTemplates    map[string]string `json:"requestTemplates,omitempty"`
	Credentials         string            `json:"credentials,omitempty"`

	IntegrationResponses map[string]*IntegrationResponse `json:"integrationResponses,omitempty"`
}

// Deployment represents an API Gateway deployment.
type Deployment struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	CreatedDate time.Time `json:"createdDate"`
}

// Stage represents an API Gateway stage.
type Stage struct {
	StageName           string                    `json:"stageName"`
	DeploymentID        string                    `json:"deploymentId"`
	Description         string                    `json:"description,omitempty"`
	CacheClusterEnabled bool                      `json:"cacheClusterEnabled,omitempty"`
	CacheClusterSize    string                    `json:"cacheClusterSize,omitempty"`
	CreatedDate         time.Time                 `json:"createdDate"`
	LastUpdatedDate     time.Time                 `json:"lastUpdatedDate"`
	Tags                map[string]string         `json:"tags,omitempty"`
	Variables           map[string]string         `json:"variables,omitempty"`
	TracingEnabled      bool                      `json:"tracingEnabled,omitempty"`
	AccessLogSettings   *AccessLogSettings        `json:"accessLogSettings,omitempty"`
	MethodSettings      map[string]*MethodSetting `json:"methodSettings,omitempty"`
}

// AccessLogSettings are a stage's access logging settings. kumo stores them
// but writes no access logs.
type AccessLogSettings struct {
	DestinationArn string `json:"destinationArn,omitempty"`
	Format         string `json:"format,omitempty"`
}

// MethodSetting is a stage's per-method (or */* wildcard) settings block,
// keyed in Stage.MethodSettings by "{resourcePath}/{httpMethod}". kumo stores
// them but enforces none.
type MethodSetting struct {
	MetricsEnabled                         bool    `json:"metricsEnabled"`
	LoggingLevel                           string  `json:"loggingLevel,omitempty"`
	DataTraceEnabled                       bool    `json:"dataTraceEnabled"`
	ThrottlingBurstLimit                   int32   `json:"throttlingBurstLimit"`
	ThrottlingRateLimit                    float64 `json:"throttlingRateLimit"`
	CachingEnabled                         bool    `json:"cachingEnabled"`
	CacheTTLInSeconds                      int32   `json:"cacheTtlInSeconds"`
	CacheDataEncrypted                     bool    `json:"cacheDataEncrypted"`
	RequireAuthorizationForCacheControl    bool    `json:"requireAuthorizationForCacheControl"`
	UnauthorizedCacheControlHeaderStrategy string  `json:"unauthorizedCacheControlHeaderStrategy,omitempty"`
}

// CreateRestAPIRequest represents a CreateRestApi request.
type CreateRestAPIRequest struct {
	Name                   string            `json:"name"`
	Description            string            `json:"description,omitempty"`
	Version                string            `json:"version,omitempty"`
	APIKeySource           string            `json:"apiKeySource,omitempty"`
	EndpointConfiguration  *EndpointConfig   `json:"endpointConfiguration,omitempty"`
	DisableExecuteAPIEndpt bool              `json:"disableExecuteApiEndpoint,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
	Policy                 string            `json:"policy,omitempty"`
	BinaryMediaTypes       []string          `json:"binaryMediaTypes,omitempty"`
	MinimumCompressionSize *int32            `json:"minimumCompressionSize,omitempty"`
}

// CreateRestAPIResponse represents a CreateRestApi response.
type CreateRestAPIResponse struct {
	ID                     string            `json:"id"`
	Name                   string            `json:"name"`
	Description            string            `json:"description,omitempty"`
	CreatedDate            float64           `json:"createdDate"`
	Version                string            `json:"version,omitempty"`
	APIKeySource           string            `json:"apiKeySource,omitempty"`
	EndpointConfiguration  *EndpointConfig   `json:"endpointConfiguration,omitempty"`
	DisableExecuteAPIEndpt bool              `json:"disableExecuteApiEndpoint,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
	RootResourceID         string            `json:"rootResourceId,omitempty"`
	Policy                 string            `json:"policy,omitempty"`
	BinaryMediaTypes       []string          `json:"binaryMediaTypes,omitempty"`
	MinimumCompressionSize *int32            `json:"minimumCompressionSize,omitempty"`
}

// GetRestAPIsResponse represents a GetRestApis response.
type GetRestAPIsResponse struct {
	Items    []CreateRestAPIResponse `json:"item,omitempty"`
	Position string                  `json:"position,omitempty"`
}

// CreateResourceRequest represents a CreateResource request.
type CreateResourceRequest struct {
	PathPart string `json:"pathPart"`
}

// ResourceResponse represents a Resource response.
type ResourceResponse struct {
	ID              string                  `json:"id"`
	ParentID        string                  `json:"parentId,omitempty"`
	PathPart        string                  `json:"pathPart,omitempty"`
	Path            string                  `json:"path"`
	ResourceMethods map[string]MethodOutput `json:"resourceMethods,omitempty"`
}

// MethodOutput represents a Method in response.
type MethodOutput struct {
	HTTPMethod        string             `json:"httpMethod,omitempty"`
	AuthorizationType string             `json:"authorizationType,omitempty"`
	AuthorizerID      string             `json:"authorizerId,omitempty"`
	APIKeyRequired    bool               `json:"apiKeyRequired,omitempty"`
	OperationName     string             `json:"operationName,omitempty"`
	MethodIntegration *IntegrationOutput `json:"methodIntegration,omitempty"`

	RequestParameters   map[string]bool            `json:"requestParameters,omitempty"`
	RequestModels       map[string]string          `json:"requestModels,omitempty"`
	RequestValidatorID  string                     `json:"requestValidatorId,omitempty"`
	AuthorizationScopes []string                   `json:"authorizationScopes,omitempty"`
	MethodResponses     map[string]*MethodResponse `json:"methodResponses,omitempty"`
}

// IntegrationOutput represents an Integration in response.
type IntegrationOutput struct {
	Type                string            `json:"type,omitempty"`
	HTTPMethod          string            `json:"httpMethod,omitempty"`
	URI                 string            `json:"uri,omitempty"`
	ConnectionType      string            `json:"connectionType,omitempty"`
	ConnectionID        string            `json:"connectionId,omitempty"`
	PassthroughBehavior string            `json:"passthroughBehavior,omitempty"`
	ContentHandling     string            `json:"contentHandling,omitempty"`
	TimeoutInMillis     int32             `json:"timeoutInMillis,omitempty"`
	CacheNamespace      string            `json:"cacheNamespace,omitempty"`
	CacheKeyParameters  []string          `json:"cacheKeyParameters,omitempty"`
	RequestParameters   map[string]string `json:"requestParameters,omitempty"`
	RequestTemplates    map[string]string `json:"requestTemplates,omitempty"`
	Credentials         string            `json:"credentials,omitempty"`

	IntegrationResponses map[string]*IntegrationResponse `json:"integrationResponses,omitempty"`
}

// GetResourcesResponse represents a GetResources response.
type GetResourcesResponse struct {
	Items    []ResourceResponse `json:"item,omitempty"`
	Position string             `json:"position,omitempty"`
}

// PutMethodRequest represents a PutMethod request.
type PutMethodRequest struct {
	AuthorizationType   string            `json:"authorizationType"`
	AuthorizerID        string            `json:"authorizerId,omitempty"`
	APIKeyRequired      bool              `json:"apiKeyRequired,omitempty"`
	OperationName       string            `json:"operationName,omitempty"`
	RequestParameters   map[string]bool   `json:"requestParameters,omitempty"`
	RequestModels       map[string]string `json:"requestModels,omitempty"`
	RequestValidatorID  string            `json:"requestValidatorId,omitempty"`
	AuthorizationScopes []string          `json:"authorizationScopes,omitempty"`
}

// CreateAuthorizerRequest represents a CreateAuthorizer request.
//
// AuthorizerResultTTLInSeconds is a pointer because an explicit 0 (caching
// disabled) and an absent field (AWS defaults to 300) are different.
type CreateAuthorizerRequest struct {
	Name                         string `json:"name"`
	Type                         string `json:"type"`
	AuthorizerURI                string `json:"authorizerUri,omitempty"`
	IdentitySource               string `json:"identitySource,omitempty"`
	AuthorizerResultTTLInSeconds *int32 `json:"authorizerResultTtlInSeconds,omitempty"`
}

// resolvedTTL returns the requested result TTL, applying the AWS default of 300
// seconds when the field is absent.
func (r *CreateAuthorizerRequest) resolvedTTL() int32 {
	if r.AuthorizerResultTTLInSeconds == nil {
		return defaultAuthorizerResultTTL
	}

	return *r.AuthorizerResultTTLInSeconds
}

// AuthorizerResponse represents an Authorizer response. The result TTL is
// always emitted: 0 means caching is disabled, which is not the same as absent.
type AuthorizerResponse struct {
	ID                           string `json:"id"`
	Name                         string `json:"name"`
	Type                         string `json:"type"`
	AuthorizerURI                string `json:"authorizerUri,omitempty"`
	IdentitySource               string `json:"identitySource,omitempty"`
	AuthorizerResultTTLInSeconds int32  `json:"authorizerResultTtlInSeconds"`
}

// GetAuthorizersResponse represents a GetAuthorizers response.
type GetAuthorizersResponse struct {
	Items    []AuthorizerResponse `json:"item,omitempty"`
	Position string               `json:"position,omitempty"`
}

// AuthorizerEvent is the REQUEST-type Lambda authorizer input event for a REST
// API. REST APIs have no payload format version setting; the 1.0 shape below is
// the only one they send.
type AuthorizerEvent struct {
	Type                  string               `json:"type"`
	MethodArn             string               `json:"methodArn"`
	Resource              string               `json:"resource"`
	Path                  string               `json:"path"`
	HTTPMethod            string               `json:"httpMethod"`
	Headers               map[string]string    `json:"headers"`
	QueryStringParameters map[string]string    `json:"queryStringParameters"`
	PathParameters        map[string]string    `json:"pathParameters"`
	StageVariables        map[string]string    `json:"stageVariables"`
	RequestContext        AuthorizerRequestCtx `json:"requestContext"`
}

// AuthorizerRequestCtx is the requestContext block of an authorizer event. AWS
// documents it as API-dependent and extensible; these are the keys its REST
// example carries that kumo can fill.
type AuthorizerRequestCtx struct {
	Path         string                    `json:"path"`
	AccountID    string                    `json:"accountId"`
	ResourceID   string                    `json:"resourceId"`
	Stage        string                    `json:"stage"`
	RequestID    string                    `json:"requestId"`
	Identity     AuthorizerRequestIdentity `json:"identity"`
	ResourcePath string                    `json:"resourcePath"`
	HTTPMethod   string                    `json:"httpMethod"`
	APIID        string                    `json:"apiId"`
}

// AuthorizerRequestIdentity is the identity block of an authorizer event's
// request context.
type AuthorizerRequestIdentity struct {
	APIKey   string `json:"apiKey"`
	SourceIP string `json:"sourceIp"`
}

// AuthorizerOutput is the REQUEST-type authorizer response for a REST API: a
// principal plus an IAM policy document, both required. The policy fields use
// AWS IAM PascalCase keys.
//
// The simple `isAuthorized` form is HTTP API only, so it is not modeled here.
type AuthorizerOutput struct {
	PrincipalID        string         `json:"principalId"`
	PolicyDocument     PolicyDocument `json:"policyDocument"`
	Context            map[string]any `json:"context,omitempty"`
	UsageIdentifierKey string         `json:"usageIdentifierKey,omitempty"`
}

// PolicyDocument is an IAM policy document.
type PolicyDocument struct {
	Version   string            `json:"Version"`
	Statement []PolicyStatement `json:"Statement"`
}

// PolicyStatement is a single IAM policy statement. Resource may be a string or
// an array of strings, so it is kept raw and normalized during evaluation.
type PolicyStatement struct {
	Action   json.RawMessage `json:"Action"`
	Effect   string          `json:"Effect"`
	Resource json.RawMessage `json:"Resource"`
}

// PutIntegrationRequest represents a PutIntegration request.
type PutIntegrationRequest struct {
	Type                string            `json:"type"`
	HTTPMethod          string            `json:"httpMethod,omitempty"`
	URI                 string            `json:"uri,omitempty"`
	ConnectionType      string            `json:"connectionType,omitempty"`
	ConnectionID        string            `json:"connectionId,omitempty"`
	PassthroughBehavior string            `json:"passthroughBehavior,omitempty"`
	ContentHandling     string            `json:"contentHandling,omitempty"`
	TimeoutInMillis     int32             `json:"timeoutInMillis,omitempty"`
	CacheNamespace      string            `json:"cacheNamespace,omitempty"`
	CacheKeyParameters  []string          `json:"cacheKeyParameters,omitempty"`
	RequestParameters   map[string]string `json:"requestParameters,omitempty"`
	RequestTemplates    map[string]string `json:"requestTemplates,omitempty"`
	Credentials         string            `json:"credentials,omitempty"`
}

// CreateDeploymentRequest represents a CreateDeployment request.
type CreateDeploymentRequest struct {
	StageName   string `json:"stageName,omitempty"`
	Description string `json:"description,omitempty"`
}

// DeploymentResponse represents a Deployment response.
type DeploymentResponse struct {
	ID          string  `json:"id"`
	Description string  `json:"description,omitempty"`
	CreatedDate float64 `json:"createdDate"`
}

// GetDeploymentsResponse represents a GetDeployments response.
type GetDeploymentsResponse struct {
	Items    []DeploymentResponse `json:"item,omitempty"`
	Position string               `json:"position,omitempty"`
}

// CreateStageRequest represents a CreateStage request.
type CreateStageRequest struct {
	StageName           string            `json:"stageName"`
	DeploymentID        string            `json:"deploymentId"`
	Description         string            `json:"description,omitempty"`
	CacheClusterEnabled bool              `json:"cacheClusterEnabled,omitempty"`
	CacheClusterSize    string            `json:"cacheClusterSize,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
	Variables           map[string]string `json:"variables,omitempty"`
	TracingEnabled      bool              `json:"tracingEnabled,omitempty"`
}

// StageResponse represents a Stage response.
type StageResponse struct {
	StageName           string                    `json:"stageName"`
	DeploymentID        string                    `json:"deploymentId"`
	Description         string                    `json:"description,omitempty"`
	CacheClusterEnabled bool                      `json:"cacheClusterEnabled,omitempty"`
	CacheClusterSize    string                    `json:"cacheClusterSize,omitempty"`
	CreatedDate         float64                   `json:"createdDate"`
	LastUpdatedDate     float64                   `json:"lastUpdatedDate"`
	Tags                map[string]string         `json:"tags,omitempty"`
	Variables           map[string]string         `json:"variables,omitempty"`
	TracingEnabled      bool                      `json:"tracingEnabled"`
	AccessLogSettings   *AccessLogSettings        `json:"accessLogSettings,omitempty"`
	MethodSettings      map[string]*MethodSetting `json:"methodSettings,omitempty"`
}

// GetStagesResponse represents a GetStages response.
type GetStagesResponse struct {
	Items []StageResponse `json:"item,omitempty"`
}

// ErrorResponse represents an API Gateway error response.
type ErrorResponse struct {
	Type    string `json:"__type,omitempty"`
	Message string `json:"message"`
}

// ServiceError represents a service error.
type ServiceError = service.CodedError
