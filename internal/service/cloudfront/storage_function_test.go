package cloudfront

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const functionTestCode = "function handler(event) { return event.request; }"

func createFunctionRequest(name string) *CreateFunctionRequest {
	return &CreateFunctionRequest{
		Name:           name,
		FunctionConfig: &FunctionConfigXML{Comment: "c", Runtime: "cloudfront-js-2.0"},
		FunctionCode:   base64.StdEncoding.EncodeToString([]byte(functionTestCode)),
	}
}

func TestFunction_Validation(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	mutate := func(f func(*CreateFunctionRequest)) *CreateFunctionRequest {
		req := createFunctionRequest("valid")
		f(req)

		return req
	}

	cases := map[string]struct {
		req  *CreateFunctionRequest
		want string
	}{
		"bad name":    {mutate(func(r *CreateFunctionRequest) { r.Name = "has space" }), errInvalidArgument},
		"long name":   {mutate(func(r *CreateFunctionRequest) { r.Name = strings.Repeat("a", functionMaxNameLength+1) }), errInvalidArgument},
		"no config":   {mutate(func(r *CreateFunctionRequest) { r.FunctionConfig = nil }), errInvalidArgument},
		"bad runtime": {mutate(func(r *CreateFunctionRequest) { r.FunctionConfig.Runtime = "nodejs20.x" }), errInvalidArgument},
		"empty code":  {mutate(func(r *CreateFunctionRequest) { r.FunctionCode = "" }), errInvalidArgument},
		"not base64":  {mutate(func(r *CreateFunctionRequest) { r.FunctionCode = "!!" }), errInvalidArgument},
		"code too large": {mutate(func(r *CreateFunctionRequest) {
			r.FunctionCode = base64.StdEncoding.EncodeToString(make([]byte, functionMaxCodeSize+1))
		}), errFunctionSizeLimitExceeded},
	}

	for name, tc := range cases {
		if _, err := store.CreateFunction(ctx, tc.req); errorCode(t, err) != tc.want {
			t.Errorf("%s: err = %v, want %s", name, err, tc.want)
		}
	}
}

func TestFunction_Lifecycle(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	created, err := store.CreateFunction(ctx, createFunctionRequest("rewrite"))
	if err != nil {
		t.Fatalf("CreateFunction: %v", err)
	}

	if created.status(functionStageDevelopment) != functionStatusUnpublished || created.Live != nil {
		t.Errorf("new function: status %s, live %v", created.status(functionStageDevelopment), created.Live)
	}

	if _, err := store.CreateFunction(ctx, createFunctionRequest("rewrite")); errorCode(t, err) != errFunctionAlreadyExists {
		t.Errorf("duplicate: err = %v", err)
	}

	if _, err := store.PublishFunction(ctx, "rewrite", "stale"); errorCode(t, err) != errPreconditionFailed {
		t.Errorf("publish with stale If-Match: err = %v", err)
	}

	published, err := store.PublishFunction(ctx, "rewrite", created.Development.ETag)
	if err != nil || published.Live == nil || string(published.Live.Code) != functionTestCode {
		t.Fatalf("PublishFunction = %+v, %v", published, err)
	}

	if got := published.status(functionStageDevelopment); got != functionStatusUnassociated {
		t.Errorf("published status = %s", got)
	}

	update := &UpdateFunctionRequest{
		FunctionConfig: &FunctionConfigXML{Comment: "v2", Runtime: "cloudfront-js-1.0"},
		FunctionCode:   base64.StdEncoding.EncodeToString([]byte("function handler(e) { return e.response; }")),
	}

	updated, err := store.UpdateFunction(ctx, "rewrite", update, published.Development.ETag)
	if err != nil {
		t.Fatalf("UpdateFunction: %v", err)
	}

	if updated.Development.Comment != "v2" || string(updated.Live.Code) != functionTestCode || updated.status(functionStageDevelopment) != functionStatusUnpublished {
		t.Errorf("update must change DEVELOPMENT only: %+v", updated)
	}

	if err := store.DeleteFunction(ctx, "rewrite", published.Development.ETag); errorCode(t, err) != errPreconditionFailed {
		t.Errorf("delete with stale If-Match: err = %v", err)
	}

	if err := store.DeleteFunction(ctx, "rewrite", updated.Development.ETag); err != nil {
		t.Fatalf("DeleteFunction: %v", err)
	}

	if _, err := store.GetFunction(ctx, "rewrite"); errorCode(t, err) != errNoSuchFunctionExists {
		t.Errorf("get after delete: err = %v", err)
	}
}

func TestFunction_DistributionAssociation(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	fn, err := store.CreateFunction(ctx, createFunctionRequest("assoc"))
	if err != nil {
		t.Fatalf("CreateFunction: %v", err)
	}

	req := distributionRequest("function-assoc", policyRefs{}, nil)
	req.DefaultCacheBehavior.FunctionAssociations = &FunctionAssociationsXML{
		Quantity: 1,
		Items:    []FunctionAssociationXML{{FunctionARN: fn.ARN, EventType: "viewer-request"}},
	}

	if _, err := store.CreateDistribution(ctx, req); errorCode(t, err) != errInvalidFunctionAssoc {
		t.Errorf("unpublished function: err = %v", err)
	}

	if _, err := store.PublishFunction(ctx, fn.Name, fn.Development.ETag); err != nil {
		t.Fatalf("PublishFunction: %v", err)
	}

	dist, err := store.CreateDistribution(ctx, req)
	if err != nil {
		t.Fatalf("CreateDistribution: %v", err)
	}

	got, err := store.GetFunction(ctx, fn.Name)
	if err != nil || got.status(functionStageLive) != functionStatusDeployed {
		t.Errorf("associated function status = %s, %v", got.status(functionStageLive), err)
	}

	if err := store.DeleteFunction(ctx, fn.Name, got.Development.ETag); errorCode(t, err) != errFunctionInUse {
		t.Errorf("delete associated function: err = %v", err)
	}

	// The association must read back, or terraform reports drift.
	out := buildDistributionConfigXML(dist.DistributionConfig).DefaultCacheBehavior.FunctionAssociations
	if out.Quantity != 1 || out.Items[0].FunctionARN != fn.ARN || out.Items[0].EventType != "viewer-request" {
		t.Errorf("FunctionAssociations read back = %+v", out)
	}

	unknown := distributionRequest("function-unknown", policyRefs{}, nil)
	unknown.DefaultCacheBehavior.FunctionAssociations = &FunctionAssociationsXML{
		Quantity: 1,
		Items:    []FunctionAssociationXML{{FunctionARN: "arn:aws:cloudfront::000000000000:function/missing", EventType: "viewer-request"}},
	}

	if _, err := store.CreateDistribution(ctx, unknown); errorCode(t, err) != errNoSuchFunctionExists {
		t.Errorf("unknown function: err = %v", err)
	}
}

func TestFunctionHandlers_StagesAndCode(t *testing.T) {
	t.Parallel()

	svc := New(NewMemoryStorage())

	body, err := xml.Marshal(createFunctionRequest("handlers"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	w := httptest.NewRecorder()
	svc.CreateFunction(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/2020-05-31/function", strings.NewReader(string(body))))

	if w.Code != http.StatusCreated || w.Header().Get("ETag") == "" || !strings.Contains(w.Body.String(), "<Status>UNPUBLISHED</Status>") {
		t.Fatalf("CreateFunction: status %d etag %q body=%s", w.Code, w.Header().Get("ETag"), w.Body.String())
	}

	devETag := w.Header().Get("ETag")

	call := func(method, target string, handler http.HandlerFunc, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody)
		req.SetPathValue("name", "handlers")

		for k, v := range hdr {
			req.Header.Set(k, v)
		}

		rec := httptest.NewRecorder()
		handler(rec, req)

		return rec
	}

	if w := call(http.MethodGet, functionPath+"handlers/describe?Stage=LIVE", svc.DescribeFunction, nil); w.Code != http.StatusNotFound {
		t.Errorf("describe LIVE before publish: status %d", w.Code)
	}

	if w := call(http.MethodGet, functionPath+"handlers?Stage=DEVELOPMENT", svc.GetFunction, nil); w.Code != http.StatusOK || w.Body.String() != functionTestCode || w.Header().Get("ETag") != devETag {
		t.Errorf("GetFunction: status %d etag %q body %q", w.Code, w.Header().Get("ETag"), w.Body.String())
	}

	w = call(http.MethodPost, functionPath+"handlers/publish", svc.PublishFunction, map[string]string{"If-Match": devETag})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<Stage>LIVE</Stage>") {
		t.Fatalf("PublishFunction: status %d body=%s", w.Code, w.Body.String())
	}

	liveETag := w.Header().Get("ETag")

	w = call(http.MethodGet, functionPath+"handlers/describe?Stage=LIVE", svc.DescribeFunction, nil)
	if w.Code != http.StatusOK || w.Header().Get("ETag") != liveETag || !strings.Contains(w.Body.String(), "<Status>UNASSOCIATED</Status>") {
		t.Errorf("describe LIVE: status %d etag %q (want %q) body=%s", w.Code, w.Header().Get("ETag"), liveETag, w.Body.String())
	}

	if w := call(http.MethodGet, "/2020-05-31/function?Stage=LIVE", svc.ListFunctions, nil); !strings.Contains(w.Body.String(), "<Quantity>1</Quantity>") {
		t.Errorf("ListFunctions(LIVE) = %s", w.Body.String())
	}

	if w := call(http.MethodGet, "/2020-05-31/function?Stage=bogus", svc.ListFunctions, nil); w.Code != http.StatusBadRequest {
		t.Errorf("ListFunctions(bogus stage): status %d", w.Code)
	}

	if w := call(http.MethodDelete, functionPath+"handlers", svc.DeleteFunction, map[string]string{"If-Match": devETag}); w.Code != http.StatusNoContent {
		t.Errorf("DeleteFunction: status %d body=%s", w.Code, w.Body.String())
	}
}

func TestDistribution_CustomErrorResponsesAndRestrictionsRoundTrip(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()

	req := distributionRequest("custom-errors", policyRefs{}, nil)
	req.CustomErrorResponses = &CustomErrorResponsesXML{Quantity: 1, Items: []CustomErrorResponseXML{
		{ErrorCode: http.StatusNotFound, ResponsePagePath: "/index.html", ResponseCode: "200", ErrorCachingMinTTL: new(int64(10))},
	}}
	req.Restrictions = &RestrictionsXML{GeoRestriction: &GeoRestrictionXML{RestrictionType: "whitelist", Quantity: 2, Items: []string{"JP", "US"}}}

	dist, err := store.CreateDistribution(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateDistribution: %v", err)
	}

	out := buildDistributionConfigXML(dist.DistributionConfig)

	if out.CustomErrorResponses.Quantity != 1 || out.CustomErrorResponses.Items[0].ResponsePagePath != "/index.html" || *out.CustomErrorResponses.Items[0].ErrorCachingMinTTL != 10 {
		t.Errorf("CustomErrorResponses = %+v", out.CustomErrorResponses)
	}

	if geo := out.Restrictions.GeoRestriction; geo.RestrictionType != "whitelist" || geo.Quantity != 2 {
		t.Errorf("GeoRestriction = %+v", geo)
	}

	bare := buildDistributionConfigXML(newBareDistribution().DistributionConfig)
	if bare.CustomErrorResponses == nil || bare.Restrictions == nil || bare.Restrictions.GeoRestriction.RestrictionType != "none" {
		t.Errorf("defaults must always be emitted: %+v %+v", bare.CustomErrorResponses, bare.Restrictions)
	}
}
