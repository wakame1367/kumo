package apigateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// muxRouter adapts http.ServeMux to service.Router so tests exercise the
// registered routes (and their path parsing) end to end.
type muxRouter struct{ mux *http.ServeMux }

func (m muxRouter) Handle(method, pattern string, h http.HandlerFunc) {
	m.mux.HandleFunc(method+" "+pattern, h)
}

func (m muxRouter) HandleFunc(method, pattern string, h http.HandlerFunc) {
	m.Handle(method, pattern, h)
}

// apiClient drives a Service through its routes.
type apiClient struct {
	t   *testing.T
	svc *Service
	mux *http.ServeMux
}

func newAPIClient(t *testing.T) *apiClient {
	t.Helper()

	svc := New(NewMemoryStorage())
	mux := http.NewServeMux()
	svc.RegisterRoutes(muxRouter{mux})

	return &apiClient{t: t, svc: svc, mux: mux}
}

// call sends a JSON request and decodes a JSON response into out (if non-nil),
// failing the test unless the status is want.
func (c *apiClient) call(method, path string, body any, want int, out any) {
	c.t.Helper()

	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			c.t.Fatalf("encode: %v", err)
		}
	}

	req := httptest.NewRequestWithContext(c.t.Context(), method, path, &buf)
	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, req)

	if w.Code != want {
		c.t.Fatalf("%s %s: status %d, want %d; body=%s", method, path, w.Code, want, w.Body.String())
	}

	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, w.Body.String(), err)
		}
	}
}

const (
	allowOriginParam = "method.response.header.Access-Control-Allow-Origin"
	allowAnyOrigin   = "'*'"
	trueValue        = "true"
	prodStage        = "prod"
)

func patchOps(ops ...PatchOperation) UpdateRequest { return UpdateRequest{PatchOperations: ops} }

// newProxyAPI creates a REST API with a /{proxy+} resource and an OPTIONS
// method, returning the API and resource ids.
func (c *apiClient) newProxyAPI(name string) (apiID, resourceID string) {
	c.t.Helper()

	var api CreateRestAPIResponse

	c.call(http.MethodPost, "/restapis", CreateRestAPIRequest{Name: name, Policy: `{"Version":"2012-10-17"}`}, http.StatusCreated, &api)

	var res ResourceResponse

	c.call(http.MethodPost, "/restapis/"+api.ID+"/resources/"+api.RootResourceID, CreateResourceRequest{PathPart: "{proxy+}"}, http.StatusCreated, &res)

	c.call(http.MethodPut, "/restapis/"+api.ID+"/resources/"+res.ID+"/methods/OPTIONS",
		PutMethodRequest{AuthorizationType: "NONE", RequestParameters: map[string]bool{"method.request.header.Origin": false}}, http.StatusCreated, nil)

	return api.ID, res.ID
}

func TestMethodAndIntegrationResponses(t *testing.T) {
	t.Parallel()

	c := newAPIClient(t)
	apiID, resID := c.newProxyAPI("responses")
	methodPath := "/restapis/" + apiID + "/resources/" + resID + "/methods/OPTIONS"

	c.call(http.MethodPut, methodPath+"/responses/200",
		PutMethodResponseRequest{ResponseParameters: map[string]bool{allowOriginParam: true}}, http.StatusCreated, nil)

	// An integration response needs an integration.
	c.call(http.MethodPut, methodPath+"/integration/responses/200", PutIntegrationResponseRequest{}, http.StatusNotFound, nil)

	c.call(http.MethodPut, methodPath+"/integration",
		PutIntegrationRequest{Type: integrationTypeMock, RequestTemplates: map[string]string{contentTypeJSON: `{"statusCode": 200}`}}, http.StatusCreated, nil)

	c.call(http.MethodPut, methodPath+"/integration/responses/200", PutIntegrationResponseRequest{
		ResponseParameters: map[string]string{allowOriginParam: allowAnyOrigin},
	}, http.StatusCreated, nil)

	var method MethodOutput

	c.call(http.MethodGet, methodPath, nil, http.StatusOK, &method)

	if !method.MethodResponses["200"].ResponseParameters[allowOriginParam] {
		t.Errorf("methodResponses = %+v", method.MethodResponses)
	}

	if got := method.MethodIntegration.IntegrationResponses["200"].ResponseParameters[allowOriginParam]; got != allowAnyOrigin {
		t.Errorf("integrationResponses = %+v", method.MethodIntegration.IntegrationResponses)
	}

	if required, ok := method.RequestParameters["method.request.header.Origin"]; !ok || required {
		t.Errorf("requestParameters = %+v", method.RequestParameters)
	}

	var ir IntegrationResponse

	c.call(http.MethodPatch, methodPath+"/integration/responses/200", patchOps(
		PatchOperation{Op: patchReplace, Path: "/selectionPattern", Value: "2\\d{2}"},
		PatchOperation{Op: patchAdd, Path: "/responseTemplates/application~1json", Value: `{"ok":true}`},
	), http.StatusOK, &ir)

	if ir.SelectionPattern != "2\\d{2}" || ir.ResponseTemplates[contentTypeJSON] != `{"ok":true}` {
		t.Errorf("UpdateIntegrationResponse = %+v", ir)
	}

	var mr MethodResponse

	c.call(http.MethodPatch, methodPath+"/responses/200", patchOps(
		PatchOperation{Op: patchRemove, Path: "/responseParameters/method.response.header.Access-Control-Allow-Origin"},
	), http.StatusOK, &mr)

	if len(mr.ResponseParameters) != 0 {
		t.Errorf("UpdateMethodResponse = %+v", mr)
	}

	c.call(http.MethodPut, methodPath+"/responses/abc", PutMethodResponseRequest{}, http.StatusBadRequest, nil)
}

func TestMethodAndIntegrationResponses_Delete(t *testing.T) {
	t.Parallel()

	c := newAPIClient(t)
	apiID, resID := c.newProxyAPI("responses-delete")
	methodPath := "/restapis/" + apiID + "/resources/" + resID + "/methods/OPTIONS"

	c.call(http.MethodPut, methodPath+"/responses/200", PutMethodResponseRequest{}, http.StatusCreated, nil)
	c.call(http.MethodPut, methodPath+"/integration", PutIntegrationRequest{Type: integrationTypeMock}, http.StatusCreated, nil)
	c.call(http.MethodPut, methodPath+"/integration/responses/200", PutIntegrationResponseRequest{}, http.StatusCreated, nil)

	c.call(http.MethodDelete, methodPath+"/integration/responses/200", nil, http.StatusAccepted, nil)
	c.call(http.MethodGet, methodPath+"/integration/responses/200", nil, http.StatusNotFound, nil)
	c.call(http.MethodDelete, methodPath+"/integration", nil, http.StatusAccepted, nil)
	c.call(http.MethodGet, methodPath+"/integration", nil, http.StatusNotFound, nil)
	c.call(http.MethodDelete, methodPath+"/responses/200", nil, http.StatusAccepted, nil)
	c.call(http.MethodGet, methodPath+"/responses/200", nil, http.StatusNotFound, nil)
}

func TestUpdateOperations(t *testing.T) {
	t.Parallel()

	c := newAPIClient(t)
	apiID, resID := c.newProxyAPI("updates")

	var api CreateRestAPIResponse

	c.call(http.MethodPatch, "/restapis/"+apiID, patchOps(
		PatchOperation{Op: patchReplace, Path: "/name", Value: "renamed"},
		PatchOperation{Op: patchAdd, Path: "/binaryMediaTypes/image~1png"},
		PatchOperation{Op: patchReplace, Path: "/minimumCompressionSize", Value: "1024"},
		PatchOperation{Op: patchReplace, Path: "/endpointConfiguration/types/EDGE", Value: "REGIONAL"},
	), http.StatusOK, &api)

	if api.Name != "renamed" || len(api.BinaryMediaTypes) != 1 || api.BinaryMediaTypes[0] != "image/png" || *api.MinimumCompressionSize != 1024 || api.EndpointConfiguration.Types[0] != "REGIONAL" {
		t.Errorf("UpdateRestApi = %+v", api)
	}

	// GetRestApi returns the policy escaped; unquoting it yields the document.
	if api.Policy != `{\"Version\":\"2012-10-17\"}` {
		t.Errorf("policy = %q", api.Policy)
	}

	c.call(http.MethodPatch, "/restapis/"+apiID, patchOps(PatchOperation{Op: patchReplace, Path: "/bogus", Value: "x"}), http.StatusBadRequest, nil)
	c.call(http.MethodPatch, "/restapis/"+apiID, patchOps(PatchOperation{Op: patchMove, Path: "/name", From: "/description"}), http.StatusBadRequest, nil)

	methodPath := "/restapis/" + apiID + "/resources/" + resID + "/methods/OPTIONS"

	var method MethodOutput

	c.call(http.MethodPatch, methodPath, patchOps(
		PatchOperation{Op: patchReplace, Path: "/apiKeyRequired", Value: trueValue},
		PatchOperation{Op: patchAdd, Path: "/requestParameters/method.request.querystring.q", Value: trueValue},
	), http.StatusOK, &method)

	if !method.APIKeyRequired || !method.RequestParameters["method.request.querystring.q"] {
		t.Errorf("UpdateMethod = %+v", method)
	}

	c.call(http.MethodPut, methodPath+"/integration", PutIntegrationRequest{Type: "HTTP_PROXY", HTTPMethod: "ANY", URI: "http://a.example"}, http.StatusCreated, nil)

	var integration IntegrationOutput

	c.call(http.MethodPatch, methodPath+"/integration", patchOps(
		PatchOperation{Op: patchReplace, Path: "/uri", Value: "http://b.example"},
		PatchOperation{Op: patchReplace, Path: "/timeoutInMillis", Value: "5000"},
		PatchOperation{Op: patchAdd, Path: "/requestParameters/integration.request.header.X", Value: "'v'"},
	), http.StatusOK, &integration)

	if integration.URI != "http://b.example" || integration.TimeoutInMillis != 5000 || integration.RequestParameters["integration.request.header.X"] != "'v'" {
		t.Errorf("UpdateIntegration = %+v", integration)
	}
}

func TestUpdateStageAndDeployment(t *testing.T) {
	t.Parallel()

	c := newAPIClient(t)
	apiID, _ := c.newProxyAPI("stages")

	var deployment DeploymentResponse

	c.call(http.MethodPost, "/restapis/"+apiID+"/deployments", CreateDeploymentRequest{}, http.StatusCreated, &deployment)
	c.call(http.MethodPost, "/restapis/"+apiID+"/stages", CreateStageRequest{StageName: prodStage, DeploymentID: deployment.ID}, http.StatusCreated, nil)

	var stage StageResponse

	c.call(http.MethodPatch, "/restapis/"+apiID+"/stages/"+prodStage, patchOps(
		PatchOperation{Op: patchReplace, Path: "/variables/env", Value: prodStage},
		PatchOperation{Op: patchReplace, Path: "/tracingEnabled", Value: trueValue},
		PatchOperation{Op: patchReplace, Path: "/*/*/metrics/enabled", Value: trueValue},
		PatchOperation{Op: patchReplace, Path: "/*/*/throttling/rateLimit", Value: "10.5"},
		PatchOperation{Op: patchReplace, Path: "/accessLogSettings/destinationArn", Value: "arn:aws:logs:us-east-1:000000000000:log-group:g"},
	), http.StatusOK, &stage)

	ms := stage.MethodSettings["*/*"]
	if stage.Variables["env"] != prodStage || !stage.TracingEnabled || ms == nil || !ms.MetricsEnabled || ms.ThrottlingRateLimit != 10.5 || stage.AccessLogSettings.DestinationArn == "" {
		t.Errorf("UpdateStage = %+v (method settings %+v)", stage, ms)
	}

	c.call(http.MethodPatch, "/restapis/"+apiID+"/stages/"+prodStage, patchOps(PatchOperation{Op: patchReplace, Path: "/deploymentId", Value: "missing"}), http.StatusNotFound, nil)

	var dep DeploymentResponse

	c.call(http.MethodPatch, "/restapis/"+apiID+"/deployments/"+deployment.ID, patchOps(PatchOperation{Op: patchReplace, Path: "/description", Value: "v2"}), http.StatusOK, &dep)

	if dep.Description != "v2" {
		t.Errorf("UpdateDeployment = %+v", dep)
	}
}

func TestUpdateResource_RepathsDescendants(t *testing.T) {
	t.Parallel()

	c := newAPIClient(t)

	var api CreateRestAPIResponse

	c.call(http.MethodPost, "/restapis", CreateRestAPIRequest{Name: "tree"}, http.StatusCreated, &api)

	create := func(parent, part string) ResourceResponse {
		var res ResourceResponse

		c.call(http.MethodPost, "/restapis/"+api.ID+"/resources/"+parent, CreateResourceRequest{PathPart: part}, http.StatusCreated, &res)

		return res
	}

	v1 := create(api.RootResourceID, "v1")
	items := create(v1.ID, "items")
	child := create(items.ID, "{id}")

	var renamed ResourceResponse

	c.call(http.MethodPatch, "/restapis/"+api.ID+"/resources/"+v1.ID, patchOps(PatchOperation{Op: patchReplace, Path: "/pathPart", Value: "v2"}), http.StatusOK, &renamed)

	var got ResourceResponse

	c.call(http.MethodGet, "/restapis/"+api.ID+"/resources/"+child.ID, nil, http.StatusOK, &got)

	if renamed.Path != "/v2" || got.Path != "/v2/items/{id}" {
		t.Errorf("paths after rename: %s, %s", renamed.Path, got.Path)
	}

	c.call(http.MethodPatch, "/restapis/"+api.ID+"/resources/"+v1.ID, patchOps(PatchOperation{Op: patchReplace, Path: "/parentId", Value: child.ID}), http.StatusBadRequest, nil)
	c.call(http.MethodPatch, "/restapis/"+api.ID+"/resources/"+api.RootResourceID, patchOps(PatchOperation{Op: patchReplace, Path: "/pathPart", Value: "x"}), http.StatusBadRequest, nil)
}

func TestExecuteAPI_MockIntegration(t *testing.T) {
	t.Parallel()

	c := newAPIClient(t)
	apiID, resID := c.newProxyAPI("mock")
	methodPath := "/restapis/" + apiID + "/resources/" + resID + "/methods/OPTIONS"

	c.call(http.MethodPut, methodPath+"/responses/200", PutMethodResponseRequest{}, http.StatusCreated, nil)
	c.call(http.MethodPut, methodPath+"/integration",
		PutIntegrationRequest{Type: integrationTypeMock, RequestTemplates: map[string]string{contentTypeJSON: `{"statusCode": 200}`}}, http.StatusCreated, nil)
	c.call(http.MethodPut, methodPath+"/integration/responses/200", PutIntegrationResponseRequest{
		ResponseParameters: map[string]string{
			allowOriginParam: allowAnyOrigin,
			"method.response.header.Access-Control-Allow-Methods": "'GET,OPTIONS'",
			"method.response.header.X-From-Integration":           "integration.response.header.X",
		},
		ResponseTemplates: map[string]string{contentTypeJSON: `{"cors":true}`},
	}, http.StatusCreated, nil)

	var deployment DeploymentResponse

	c.call(http.MethodPost, "/restapis/"+apiID+"/deployments", CreateDeploymentRequest{StageName: prodStage}, http.StatusCreated, &deployment)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/prod/anything/here", http.NoBody)
	w := httptest.NewRecorder()

	if !c.svc.HandleExecuteAPI(w, req, apiID, "/prod/anything/here") {
		t.Fatal("HandleExecuteAPI did not claim the API")
	}

	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Methods") != "GET,OPTIONS" {
		t.Fatalf("MOCK OPTIONS: status %d headers %v", w.Code, w.Header())
	}

	if w.Header().Get("X-From-Integration") != "" || strings.TrimSpace(w.Body.String()) != `{"cors":true}` {
		t.Errorf("MOCK body %q, X-From-Integration %q", w.Body.String(), w.Header().Get("X-From-Integration"))
	}
}

func TestSelectIntegrationResponse(t *testing.T) {
	t.Parallel()

	responses := map[string]*IntegrationResponse{
		"200": {StatusCode: "200"},
		"400": {StatusCode: "400", SelectionPattern: "4\\d{2}"},
		"500": {StatusCode: "500", SelectionPattern: "5.*"},
	}

	for status, want := range map[string]string{"200": "200", "404": "400", "503": "500", "302": "200"} {
		got, ok := selectIntegrationResponse(responses, status)
		if !ok || got.StatusCode != want {
			t.Errorf("status %s -> %+v, want %s", status, got, want)
		}
	}

	if _, ok := selectIntegrationResponse(map[string]*IntegrationResponse{"400": {StatusCode: "400", SelectionPattern: "4.."}}, "200"); ok {
		t.Error("no default response must not match")
	}
}
