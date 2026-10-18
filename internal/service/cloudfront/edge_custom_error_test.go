package cloudfront

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newSPADistribution creates a distribution in front of a fake origin that
// serves /index.html and 404s everything else, configured like a typical
// single-page app: DefaultRootObject plus a 404 -> /index.html (200) custom
// error response.
func newSPADistribution(t *testing.T, responseCode string) (*Service, string) {
	t.Helper()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<h1>app</h1>"))

			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(origin.Close)

	host, port := splitHostPort(strings.TrimPrefix(origin.URL, "http://"))

	svc := New(NewMemoryStorage())

	dist, err := svc.storage.CreateDistribution(context.Background(), &CreateDistributionRequest{
		CallerReference:   "spa",
		Enabled:           true,
		DefaultRootObject: "index.html",
		Origins: &OriginsXML{Quantity: 1, Items: &OriginList{Origin: []OriginXML{{
			ID: "app", DomainName: host,
			CustomOriginConfig: &CustomOriginConfigXML{HTTPPort: port, OriginProtocolPolicy: originPolicyHTTP},
		}}}},
		DefaultCacheBehavior: &DefaultCacheBehaviorXML{TargetOriginID: "app"},
		CustomErrorResponses: &CustomErrorResponsesXML{Quantity: 1, Items: []CustomErrorResponseXML{
			{ErrorCode: http.StatusNotFound, ResponsePagePath: "/index.html", ResponseCode: responseCode},
		}},
	})
	if err != nil {
		t.Fatalf("CreateDistribution: %v", err)
	}

	return svc, dist.ID
}

func edgeRequest(t *testing.T, svc *Service, distID, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, "/kumo/cdn/"+distID+"/"+path, http.NoBody)
	req.SetPathValue("distributionId", distID)
	req.SetPathValue("path", path)

	w := httptest.NewRecorder()
	svc.Edge(w, req)

	return w
}

func TestEdge_DefaultRootObject(t *testing.T) {
	t.Parallel()

	svc, distID := newSPADistribution(t, "200")

	if w := edgeRequest(t, svc, distID, http.MethodGet, ""); w.Code != http.StatusOK || w.Body.String() != "<h1>app</h1>" {
		t.Errorf("GET / = %d %q, want the default root object", w.Code, w.Body.String())
	}
}

func TestEdge_CustomErrorResponse(t *testing.T) {
	t.Parallel()

	svc, distID := newSPADistribution(t, "200")

	w := edgeRequest(t, svc, distID, http.MethodGet, "deep/link")
	if w.Code != http.StatusOK || w.Body.String() != "<h1>app</h1>" || w.Header().Get("Content-Type") != "text/html" {
		t.Errorf("GET /deep/link = %d %q (%s), want the error page with 200", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}

	keep, keepID := newSPADistribution(t, "")
	if w := edgeRequest(t, keep, keepID, http.MethodGet, "missing"); w.Code != http.StatusNotFound || w.Body.String() != "<h1>app</h1>" {
		t.Errorf("empty ResponseCode must keep the origin status: %d %q", w.Code, w.Body.String())
	}
}

func TestEdge_OptionsPassesThrough(t *testing.T) {
	t.Parallel()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions {
			t.Errorf("origin saw %s, want OPTIONS", r.Method)
		}

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(origin.Close)

	svc := setupEdge(t, origin.URL)

	var distID string
	for id := range svc.storage.(*MemoryStorage).Distributions {
		distID = id
	}

	w := edgeRequest(t, svc, distID, http.MethodOptions, "api/items")
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("X-Cache") != "Bypass from kumo" {
		t.Errorf("OPTIONS = %d, headers %v", w.Code, w.Header())
	}
}
