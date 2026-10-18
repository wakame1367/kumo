package execapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBuildEventV1_PathIsRequestPath checks that a REST proxy event carries
// the requested path in path / requestContext.path and the resource template
// in resource / requestContext.resourcePath, as API Gateway sends them.
const proxyResource = "/{proxy+}"

func TestBuildEventV1_PathIsRequestPath(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/prod/api/hello?x=1", http.NoBody)

	data, err := buildEventV1(r, &Request{
		APIID:          "abc",
		Stage:          "prod",
		ResourcePath:   proxyResource,
		RequestPath:    "/api/hello",
		PathParameters: map[string]string{"proxy": "api/hello"},
	}, nil)
	if err != nil {
		t.Fatalf("buildEventV1: %v", err)
	}

	var ev proxyEventV1
	if err := json.Unmarshal(data, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if ev.Path != "/api/hello" || ev.Resource != proxyResource || ev.RequestContext.Path != "/prod/api/hello" || ev.RequestContext.ResourcePath != proxyResource {
		t.Errorf("event paths: path=%q resource=%q ctx.path=%q ctx.resourcePath=%q", ev.Path, ev.Resource, ev.RequestContext.Path, ev.RequestContext.ResourcePath)
	}
}
