package cloudfront

// managedOriginRequestPolicies are the AWS-managed origin request policies a
// distribution may reference without creating them.
//
// Reference: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/using-managed-origin-request-policies.html
var managedOriginRequestPolicies = map[string]string{
	"216adef6-5c7f-47e4-b989-5492eafa07d3": "Managed-AllViewer",
	"33f36d7e-f396-46d9-90e0-52428a34d9dc": "Managed-AllViewerAndCloudFrontHeaders-2022-06",
	"b689b0a8-53d0-40ab-baf2-68738e2966ac": "Managed-AllViewerExceptHostHeader",
	"59781a5b-3903-41f3-afcb-af62929ccde1": "Managed-CORS-CustomOrigin",
	"88a5eaf4-2fd4-4709-b370-b4c650ea3fcf": "Managed-CORS-S3Origin",
	"775133bc-15f2-49f9-abea-afb2e0bf67d2": "Managed-Elemental-MediaTailor-PersonalizedManifests",
	"acba4595-bd28-49b8-b9fe-13317c0390fa": "Managed-UserAgentRefererHeaders",
}

// managedOriginRequestPolicySpec is the documented configuration of one
// AWS-managed origin request policy.
type managedOriginRequestPolicySpec struct {
	id, comment    string
	headerBehavior string
	headers        []string
	cookieBehavior string
	queryBehavior  string
}

// managedOriginRequestPolicySpecs lists the managed origin request policies
// that can be read back (GetOriginRequestPolicy, ListOriginRequestPolicies),
// as the aws_cloudfront_origin_request_policy data source does by name.
// Managed-AllViewerAndCloudFrontHeaders-2022-06 is referenceable but not
// readable: its CloudFront header list changes over time.
var managedOriginRequestPolicySpecs = []managedOriginRequestPolicySpec{
	{
		id: "216adef6-5c7f-47e4-b989-5492eafa07d3", comment: "Policy to forward all parameters in viewer requests",
		headerBehavior: originRequestHeaderAllViewer, cookieBehavior: cachePolicyBehaviorAll, queryBehavior: cachePolicyBehaviorAll,
	},
	{
		id: "b689b0a8-53d0-40ab-baf2-68738e2966ac", comment: "Policy to forward all parameters in viewer requests except for the Host header",
		headerBehavior: cachePolicyBehaviorAllExcept, headers: []string{viewerHeaderHost},
		cookieBehavior: cachePolicyBehaviorAll, queryBehavior: cachePolicyBehaviorAll,
	},
	{
		id: "59781a5b-3903-41f3-afcb-af62929ccde1", comment: "Policy for Custom Origin with CORS",
		headerBehavior: cachePolicyBehaviorWhitelist, headers: []string{viewerHeaderOrigin},
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorNone,
	},
	{
		id: "88a5eaf4-2fd4-4709-b370-b4c650ea3fcf", comment: "Policy for S3 origin with CORS",
		headerBehavior: cachePolicyBehaviorWhitelist, headers: []string{viewerHeaderOrigin, "access-control-request-headers", "access-control-request-method"},
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorNone,
	},
	{
		id: "775133bc-15f2-49f9-abea-afb2e0bf67d2", comment: "Policy for Elemental MediaTailor Personalized Manifests",
		headerBehavior: cachePolicyBehaviorWhitelist, headers: []string{viewerHeaderOrigin, "access-control-request-headers", "x-forwarded-for", "access-control-request-method", "user-agent"},
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorAll,
	},
	{
		id: "acba4595-bd28-49b8-b9fe-13317c0390fa", comment: "Policy to forward user-agent and referer headers to origin",
		headerBehavior: cachePolicyBehaviorWhitelist, headers: []string{"user-agent", "referer"},
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorAll,
	},
}

// managedOriginRequestPolicy returns the readable AWS-managed origin request
// policy with the given id.
func managedOriginRequestPolicy(id string) (*OriginRequestPolicy, bool) {
	for i := range managedOriginRequestPolicySpecs {
		if managedOriginRequestPolicySpecs[i].id == id {
			return managedOriginRequestPolicySpecs[i].policy(), true
		}
	}

	return nil, false
}

// listManagedOriginRequestPolicies returns every readable AWS-managed origin
// request policy.
func listManagedOriginRequestPolicies() []*OriginRequestPolicy {
	out := make([]*OriginRequestPolicy, 0, len(managedOriginRequestPolicySpecs))

	for i := range managedOriginRequestPolicySpecs {
		out = append(out, managedOriginRequestPolicySpecs[i].policy())
	}

	return out
}

func (m *managedOriginRequestPolicySpec) policy() *OriginRequestPolicy {
	headers := &OriginRequestPolicyHeadersConfig{HeaderBehavior: m.headerBehavior}
	if len(m.headers) > 0 {
		headers.Headers = policyNames(m.headers)
	}

	return &OriginRequestPolicy{
		ID:               m.id,
		ETag:             managedPolicyETag,
		LastModifiedTime: managedPolicyLastModified,
		managed:          true,
		Config: OriginRequestPolicyConfig{
			Comment:            m.comment,
			Name:               managedOriginRequestPolicies[m.id],
			HeadersConfig:      headers,
			CookiesConfig:      &OriginRequestPolicyCookiesConfig{CookieBehavior: m.cookieBehavior},
			QueryStringsConfig: &OriginRequestPolicyQueryStrings{QueryStringBehavior: m.queryBehavior},
		},
	}
}
