package cloudfront

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const managedAllViewerExceptHostID = "b689b0a8-53d0-40ab-baf2-68738e2966ac"

func validOriginRequestPolicy(name string) *OriginRequestPolicyConfig {
	return &OriginRequestPolicyConfig{
		Name:               name,
		HeadersConfig:      &OriginRequestPolicyHeadersConfig{HeaderBehavior: cachePolicyBehaviorWhitelist, Headers: policyNames([]string{"Origin"})},
		CookiesConfig:      &OriginRequestPolicyCookiesConfig{CookieBehavior: cachePolicyBehaviorNone},
		QueryStringsConfig: &OriginRequestPolicyQueryStrings{QueryStringBehavior: cachePolicyBehaviorAll},
	}
}

func TestValidateOriginRequestPolicyConfig(t *testing.T) {
	t.Parallel()

	mutate := func(f func(*OriginRequestPolicyConfig)) *OriginRequestPolicyConfig {
		cfg := validOriginRequestPolicy("n")
		f(cfg)

		return cfg
	}

	cases := map[string]struct {
		cfg  *OriginRequestPolicyConfig
		want string
	}{
		"empty name":        {mutate(func(c *OriginRequestPolicyConfig) { c.Name = "" }), errInvalidArgument},
		"missing headers":   {mutate(func(c *OriginRequestPolicyConfig) { c.HeadersConfig = nil }), errInvalidArgument},
		"missing cookies":   {mutate(func(c *OriginRequestPolicyConfig) { c.CookiesConfig = nil }), errInvalidArgument},
		"missing query":     {mutate(func(c *OriginRequestPolicyConfig) { c.QueryStringsConfig = nil }), errInvalidArgument},
		"bad header mode":   {mutate(func(c *OriginRequestPolicyConfig) { c.HeadersConfig.HeaderBehavior = cachePolicyBehaviorAll }), errInvalidArgument},
		"bad cookie mode":   {mutate(func(c *OriginRequestPolicyConfig) { c.CookiesConfig.CookieBehavior = originRequestHeaderAllViewer }), errInvalidArgument},
		"quantity mismatch": {mutate(func(c *OriginRequestPolicyConfig) { c.HeadersConfig.Headers.Quantity = new(2) }), errInconsistentQuantities},
	}

	for name, tc := range cases {
		if err := validateOriginRequestPolicyConfig(tc.cfg); err == nil || errorCode(t, err) != tc.want {
			t.Errorf("%s: err = %v, want %s", name, err, tc.want)
		}
	}

	for _, behavior := range []string{originRequestHeaderAllViewer, originRequestHeaderAllViewerAndCFFront, cachePolicyBehaviorAllExcept} {
		cfg := validOriginRequestPolicy("n")
		cfg.HeadersConfig.HeaderBehavior = behavior

		if err := validateOriginRequestPolicyConfig(cfg); err != nil {
			t.Errorf("header behavior %s rejected: %v", behavior, err)
		}
	}
}

func TestOriginRequestPolicy_Lifecycle(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	created, err := store.CreateOriginRequestPolicy(ctx, validOriginRequestPolicy("lifecycle"))
	if err != nil {
		t.Fatalf("CreateOriginRequestPolicy: %v", err)
	}

	if _, err := store.CreateOriginRequestPolicy(ctx, validOriginRequestPolicy("lifecycle")); errorCode(t, err) != errOriginRequestPolicyAlreadyExists {
		t.Errorf("duplicate name: err = %v", err)
	}

	update := validOriginRequestPolicy("renamed")
	update.CookiesConfig.CookieBehavior = cachePolicyBehaviorAll

	if _, err := store.UpdateOriginRequestPolicy(ctx, created.ID, update, ""); errorCode(t, err) != errInvalidIfMatchVersion {
		t.Errorf("update without If-Match: err = %v", err)
	}

	if _, err := store.UpdateOriginRequestPolicy(ctx, created.ID, update, "stale"); errorCode(t, err) != errPreconditionFailed {
		t.Errorf("update with stale If-Match: err = %v", err)
	}

	updated, err := store.UpdateOriginRequestPolicy(ctx, created.ID, update, created.ETag)
	if err != nil || updated.ETag == created.ETag || updated.Config.Name != "renamed" || updated.Config.CookiesConfig.CookieBehavior != cachePolicyBehaviorAll {
		t.Fatalf("UpdateOriginRequestPolicy = %+v, %v", updated, err)
	}

	// A distribution referencing the policy keeps it from being deleted.
	req := distributionRequest("orp-in-use", policyRefs{}, nil)
	req.DefaultCacheBehavior.OriginRequestPolicyID = created.ID

	dist, err := store.CreateDistribution(ctx, req)
	if err != nil {
		t.Fatalf("CreateDistribution: %v", err)
	}

	if err := store.DeleteOriginRequestPolicy(ctx, created.ID, updated.ETag); errorCode(t, err) != errOriginRequestPolicyInUse {
		t.Errorf("delete in use: err = %v", err)
	}

	if err := store.DeleteDistribution(ctx, dist.ID, dist.ETag); err != nil {
		t.Fatalf("DeleteDistribution: %v", err)
	}

	if err := store.DeleteOriginRequestPolicy(ctx, created.ID, updated.ETag); err != nil {
		t.Fatalf("DeleteOriginRequestPolicy: %v", err)
	}

	if _, err := store.GetOriginRequestPolicy(ctx, created.ID); errorCode(t, err) != errNoSuchOriginRequestPolicy {
		t.Errorf("get after delete: err = %v", err)
	}
}

func TestOriginRequestPolicy_DistributionReference(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	missing := distributionRequest("orp-missing", policyRefs{}, nil)
	missing.DefaultCacheBehavior.OriginRequestPolicyID = missingPolicyID

	if _, err := store.CreateDistribution(ctx, missing); errorCode(t, err) != errNoSuchOriginRequestPolicy {
		t.Errorf("unknown policy: err = %v", err)
	}

	managed := distributionRequest("orp-managed", policyRefs{}, nil)
	managed.DefaultCacheBehavior.OriginRequestPolicyID = managedAllViewerExceptHostID

	if _, err := store.CreateDistribution(ctx, managed); err != nil {
		t.Errorf("managed policy reference: %v", err)
	}
}

func TestOriginRequestPolicy_Managed(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	got, err := store.GetOriginRequestPolicy(ctx, managedAllViewerExceptHostID)
	if err != nil {
		t.Fatalf("GetOriginRequestPolicy(managed): %v", err)
	}

	if got.Config.Name != "Managed-AllViewerExceptHostHeader" || got.Config.HeadersConfig.HeaderBehavior != cachePolicyBehaviorAllExcept || got.summaryType() != cachePolicyTypeManaged {
		t.Errorf("managed policy = %+v", got.Config)
	}

	if _, err := store.UpdateOriginRequestPolicy(ctx, managedAllViewerExceptHostID, validOriginRequestPolicy("x"), managedPolicyETag); errorCode(t, err) != errIllegalUpdate {
		t.Errorf("update managed: err = %v", err)
	}

	if err := store.DeleteOriginRequestPolicy(ctx, managedAllViewerExceptHostID, managedPolicyETag); errorCode(t, err) != errIllegalDelete {
		t.Errorf("delete managed: err = %v", err)
	}

	if _, err := store.CreateOriginRequestPolicy(ctx, validOriginRequestPolicy("custom")); err != nil {
		t.Fatalf("CreateOriginRequestPolicy: %v", err)
	}

	for policyType, want := range map[string]int{
		cachePolicyTypeManaged: len(managedOriginRequestPolicySpecs),
		cachePolicyTypeCustom:  1,
		"":                     len(managedOriginRequestPolicySpecs) + 1,
	} {
		items, _, err := store.ListOriginRequestPolicies(ctx, policyType, "", 0)
		if err != nil || len(items) != want {
			t.Errorf("ListOriginRequestPolicies(%q) = %d items, %v; want %d", policyType, len(items), err, want)
		}
	}
}

// TestOriginRequestPolicy_DataSourceLookup mirrors how the
// aws_cloudfront_origin_request_policy data source resolves a name: list every
// policy, match the name, then read it by id.
func TestOriginRequestPolicy_DataSourceLookup(t *testing.T) {
	t.Parallel()

	svc := New(NewMemoryStorage())

	w := httptest.NewRecorder()
	svc.ListOriginRequestPolicies(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/2020-05-31/origin-request-policy", http.NoBody))

	if w.Code != http.StatusOK {
		t.Fatalf("ListOriginRequestPolicies: status %d body=%s", w.Code, w.Body.String())
	}

	want := `<OriginRequestPolicySummary><Type>managed</Type><OriginRequestPolicy><Id>` + managedAllViewerExceptHostID + `</Id>`
	if got := w.Body.String(); !strings.Contains(got, want) || !strings.Contains(got, "<Name>Managed-AllViewerExceptHostHeader</Name>") {
		t.Fatalf("list body = %s", got)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, originRequestPolicyPath+managedAllViewerExceptHostID, http.NoBody)
	req.SetPathValue("id", managedAllViewerExceptHostID)

	w = httptest.NewRecorder()
	svc.GetOriginRequestPolicy(w, req)

	if w.Code != http.StatusOK || w.Header().Get("ETag") == "" {
		t.Fatalf("GetOriginRequestPolicy: status %d etag %q body=%s", w.Code, w.Header().Get("ETag"), w.Body.String())
	}

	if got := w.Body.String(); !strings.Contains(got, "<HeaderBehavior>allExcept</HeaderBehavior><Headers><Quantity>1</Quantity><Items><Name>host</Name></Items></Headers>") {
		t.Errorf("get body = %s", got)
	}
}
