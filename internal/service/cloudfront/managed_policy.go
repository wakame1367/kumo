package cloudfront

import "time"

// AWS-managed policies exist in every account without being created, so a
// distribution may reference them although kumo never stores them. Only the
// IDs AWS documents are listed; the name is kept for readers.
//
// References:
//   - https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/using-managed-cache-policies.html
//   - https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/using-managed-origin-request-policies.html
//   - https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/using-managed-response-headers-policies.html
var (
	managedCachePolicies = map[string]string{
		"2e54312d-136d-493c-8eb9-b001f22f67d2": "Managed-Amplify",
		"4135ea2d-6df8-44a3-9df3-4b5a84be39ad": "Managed-CachingDisabled",
		"658327ea-f89d-4fab-a63d-7e88639e58f6": "Managed-CachingOptimized",
		"b2884449-e4de-46a7-ac36-70bc7f1ddd6d": "Managed-CachingOptimizedForUncompressedObjects",
		"08627262-05a9-4f76-9ded-b50ca2e3a84f": "Managed-Elemental-MediaPackage",
		"83da9c7e-98b4-4e11-a168-04f0df8e2c65": "UseOriginCacheControlHeaders",
		"4cc15a8a-d715-48a4-82b8-cc0b614638fe": "UseOriginCacheControlHeaders-QueryStrings",

		// AWS marks the Amplify Hosting policies as used by Amplify only.
		"4d1d2f1d-3a71-49ad-9e08-7ea5d843a556": "Amplify-Default",
		"a6bad946-36c3-4c33-aa98-362c74a7fb13": "Amplify-DefaultNoCookies",
		"1c6db51a-a33f-469a-8245-dae26771f530": "Amplify-ImageOptimization",
		"7e5fad67-ee98-4ad0-b05a-394999eefc1a": "Amplify-StaticContent",
	}

	managedResponseHeadersPolicies = map[string]string{
		"5cc3b908-e619-4b99-88e5-2cf7f45965bd": "Managed-CORS-With-Preflight",
		"e61eb60c-9c35-4d20-a928-2b84e02af89c": "Managed-CORS-and-SecurityHeadersPolicy",
		"eaab4381-ed33-4a86-88ca-d9558dc6cd63": "Managed-CORS-with-preflight-and-SecurityHeadersPolicy",
		"67f7725c-6f97-4210-82d7-5512b31e9d03": "Managed-SecurityHeadersPolicy",
		"60669652-455b-4ae9-85a4-c4c02393f86c": "Managed-SimpleCORS",
	}
)

// managedPolicyLastModified is the LastModifiedTime reported for every
// AWS-managed policy; AWS does not change them, so a fixed instant keeps
// reads stable.
var managedPolicyLastModified = time.Date(2020, time.July, 13, 0, 0, 0, 0, time.UTC)

// managedCachePolicySpec is the documented configuration of one AWS-managed
// cache policy.
type managedCachePolicySpec struct {
	id, comment            string
	minTTL, defTTL, maxTTL int64
	gzip, brotli           bool
	headers                []string
	cookieBehavior         string
	queryBehavior          string
	queryStrings           []string
}

// managedCachePolicySpecs lists the managed cache policies whose settings
// AWS documents, so the policies can be read back (GetCachePolicy,
// ListCachePolicies) exactly as the aws_cloudfront_cache_policy data source
// expects. The Amplify Hosting policies are referenceable (see
// managedCachePolicies) but not readable, as their settings are not
// documented.
var managedCachePolicySpecs = []managedCachePolicySpec{
	{
		id: "658327ea-f89d-4fab-a63d-7e88639e58f6", comment: "Policy with caching enabled. Supports Gzip and Brotli compression.",
		minTTL: 1, defTTL: 86400, maxTTL: 31536000, gzip: true, brotli: true,
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorNone,
	},
	{
		id: "4135ea2d-6df8-44a3-9df3-4b5a84be39ad", comment: "Policy with caching disabled",
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorNone,
	},
	{
		id: "b2884449-e4de-46a7-ac36-70bc7f1ddd6d", comment: "Default policy when compression is disabled",
		minTTL: 1, defTTL: 86400, maxTTL: 31536000,
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorNone,
	},
	{
		id: "08627262-05a9-4f76-9ded-b50ca2e3a84f", comment: "Policy for Elemental MediaPackage Origin",
		defTTL: 86400, maxTTL: 31536000, gzip: true,
		headers:        []string{"origin"},
		cookieBehavior: cachePolicyBehaviorNone, queryBehavior: cachePolicyBehaviorWhitelist,
		queryStrings: []string{"aws.manifestfilter", "start", "end", "m"},
	},
	{
		id: "2e54312d-136d-493c-8eb9-b001f22f67d2", comment: "Policy for Amplify Origin",
		minTTL: 2, defTTL: 2, maxTTL: 31536000, gzip: true, brotli: true,
		headers:        []string{"Authorization", "CloudFront-Viewer-Country", "Host"},
		cookieBehavior: cachePolicyBehaviorAll, queryBehavior: cachePolicyBehaviorAll,
	},
	{
		id: "83da9c7e-98b4-4e11-a168-04f0df8e2c65", comment: "Policy for origins that return Cache-Control headers. Query strings are not included in the cache key.",
		maxTTL: 31536000, gzip: true, brotli: true,
		headers:        []string{"host", "origin", "x-http-method-override", "x-http-method", "x-method-override"},
		cookieBehavior: cachePolicyBehaviorAll, queryBehavior: cachePolicyBehaviorNone,
	},
	{
		id: "4cc15a8a-d715-48a4-82b8-cc0b614638fe", comment: "Policy for origins that return Cache-Control headers and serve different content based on query strings.",
		maxTTL: 31536000, gzip: true, brotli: true,
		headers:        []string{"host", "origin", "x-http-method-override", "x-http-method", "x-method-override"},
		cookieBehavior: cachePolicyBehaviorAll, queryBehavior: cachePolicyBehaviorAll,
	},
}

// managedCachePolicy returns the readable AWS-managed cache policy with the
// given id.
func managedCachePolicy(id string) (*CachePolicy, bool) {
	for i := range managedCachePolicySpecs {
		if managedCachePolicySpecs[i].id == id {
			return managedCachePolicySpecs[i].policy(), true
		}
	}

	return nil, false
}

// listManagedCachePolicies returns every readable AWS-managed cache policy.
func listManagedCachePolicies() []*CachePolicy {
	out := make([]*CachePolicy, 0, len(managedCachePolicySpecs))

	for i := range managedCachePolicySpecs {
		out = append(out, managedCachePolicySpecs[i].policy())
	}

	return out
}

func (m *managedCachePolicySpec) policy() *CachePolicy {
	headers := &CachePolicyHeadersConfig{HeaderBehavior: cachePolicyBehaviorNone}
	if len(m.headers) > 0 {
		headers = &CachePolicyHeadersConfig{HeaderBehavior: cachePolicyBehaviorWhitelist, Headers: policyNames(m.headers)}
	}

	query := &CachePolicyQueryStrings{QueryStringBehavior: m.queryBehavior}
	if len(m.queryStrings) > 0 {
		query.QueryStrings = policyNames(m.queryStrings)
	}

	return &CachePolicy{
		ID:               m.id,
		ETag:             managedPolicyETag,
		LastModifiedTime: managedPolicyLastModified,
		managed:          true,
		Config: CachePolicyConfig{
			Comment:    m.comment,
			Name:       managedCachePolicies[m.id],
			DefaultTTL: new(m.defTTL),
			MaxTTL:     new(m.maxTTL),
			MinTTL:     new(m.minTTL),
			Parameters: &CachePolicyParameters{
				EnableAcceptEncodingGzip:   new(m.gzip),
				EnableAcceptEncodingBrotli: new(m.brotli),
				HeadersConfig:              headers,
				CookiesConfig:              &CachePolicyCookiesConfig{CookieBehavior: m.cookieBehavior},
				QueryStringsConfig:         query,
			},
		},
	}
}

// managedPolicyETag is the ETag reported for every AWS-managed policy. They
// cannot be updated, so the value never has to change.
const managedPolicyETag = "EMANAGEDPOLICY"

// policyNames builds the Quantity + Items>Name list shared by cache and
// origin request policies.
func policyNames(names []string) *CachePolicyNames {
	return &CachePolicyNames{Quantity: new(len(names)), Items: &CachePolicyNameItems{Name: append([]string(nil), names...)}}
}
