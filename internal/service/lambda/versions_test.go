package lambda

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// versionsRouter adapts http.ServeMux to service.Router.
type versionsRouter struct{ mux *http.ServeMux }

func (m versionsRouter) Handle(method, pattern string, h http.HandlerFunc) {
	m.mux.HandleFunc(method+" "+pattern, h)
}

func (m versionsRouter) HandleFunc(method, pattern string, h http.HandlerFunc) {
	m.Handle(method, pattern, h)
}

type lambdaClient struct {
	t   *testing.T
	mux *http.ServeMux
	svc *Service
}

func newLambdaClient(t *testing.T) *lambdaClient {
	t.Helper()

	svc := New(NewMemoryStorage(defaultBaseURL), defaultBaseURL)

	t.Cleanup(func() { _ = svc.Close() })

	mux := http.NewServeMux()
	svc.registerAPIRoutes(versionsRouter{mux})

	return &lambdaClient{t: t, mux: mux, svc: svc}
}

func (c *lambdaClient) call(method, path string, body any, want int, out any) {
	c.t.Helper()

	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			c.t.Fatalf("encode: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, httptest.NewRequestWithContext(c.t.Context(), method, path, &buf))

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
	fnPath   = "/2015-03-31/functions/"
	testRole = "arn:aws:iam::000000000000:role/r"
)

func TestVersions_PublishAndList(t *testing.T) {
	t.Parallel()

	c := newLambdaClient(t)

	var created CreateFunctionResponse

	c.call(http.MethodPost, "/2015-03-31/functions", CreateFunctionRequest{
		FunctionName: "versioned", Role: testRole, Runtime: "nodejs20.x", Handler: "index.handler",
		Code: FunctionCode{ZipFile: []byte("v1")}, Publish: true,
	}, http.StatusCreated, &created)

	if created.Version != "1" {
		t.Errorf("CreateFunction(Publish) Version = %q, want 1", created.Version)
	}

	// Publishing unchanged code returns the existing version.
	var same FunctionConfiguration

	c.call(http.MethodPost, fnPath+"versioned/versions", publishVersionRequest{}, http.StatusCreated, &same)

	if same.Version != "1" {
		t.Errorf("republish unchanged = version %q, want 1", same.Version)
	}

	c.call(http.MethodPost, fnPath+"versioned/versions", publishVersionRequest{CodeSha256: "stale"}, http.StatusPreconditionFailed, nil)

	var updated CreateFunctionResponse

	c.call(http.MethodPut, fnPath+"versioned/code", UpdateFunctionCodeRequest{ZipFile: []byte("v2"), Publish: true}, http.StatusOK, &updated)

	if updated.Version != "2" {
		t.Errorf("UpdateFunctionCode(Publish) Version = %q, want 2", updated.Version)
	}

	var list listVersionsByFunctionResponse

	c.call(http.MethodGet, fnPath+"versioned/versions", nil, http.StatusOK, &list)

	if len(list.Versions) != 3 || list.Versions[0].Version != latestVersion || list.Versions[2].Version != "2" ||
		list.Versions[2].FunctionArn != created.FunctionArn+":2" || list.Versions[1].CodeSha256 == list.Versions[2].CodeSha256 {
		t.Fatalf("ListVersionsByFunction = %+v", list.Versions)
	}

	var v1 FunctionConfiguration

	c.call(http.MethodGet, fnPath+"versioned/configuration?Qualifier=1", nil, http.StatusOK, &v1)

	if v1.Version != "1" || v1.CodeSha256 != list.Versions[1].CodeSha256 {
		t.Errorf("GetFunctionConfiguration(Qualifier=1) = %+v", v1)
	}

	c.call(http.MethodGet, fnPath+"versioned/configuration?Qualifier=9", nil, http.StatusNotFound, nil)
}

func TestVersions_DeleteVersionKeepsFunction(t *testing.T) {
	t.Parallel()

	c := newLambdaClient(t)

	c.call(http.MethodPost, "/2015-03-31/functions", CreateFunctionRequest{
		FunctionName: "versioned", Role: testRole, Code: FunctionCode{ZipFile: []byte("v1")}, Publish: true,
	}, http.StatusCreated, nil)

	c.call(http.MethodDelete, fnPath+"versioned?Qualifier=1", nil, http.StatusNoContent, nil)
	c.call(http.MethodGet, fnPath+"versioned/configuration?Qualifier=1", nil, http.StatusNotFound, nil)

	var latest FunctionConfiguration

	c.call(http.MethodGet, fnPath+"versioned/configuration", nil, http.StatusOK, &latest)

	if latest.Version != latestVersion {
		t.Errorf("deleting a version must keep the function: %+v", latest)
	}
}

func TestAliases_Lifecycle(t *testing.T) {
	t.Parallel()

	c := newLambdaClient(t)

	c.call(http.MethodPost, "/2015-03-31/functions", CreateFunctionRequest{
		FunctionName: "aliased", Role: testRole, Code: FunctionCode{ZipFile: []byte("v1")}, Publish: true,
	}, http.StatusCreated, nil)

	version := "1"
	description := "live traffic"

	var alias aliasConfiguration

	c.call(http.MethodPost, fnPath+"aliased/aliases", aliasRequest{Name: "live", FunctionVersion: &version, Description: &description}, http.StatusCreated, &alias)

	if alias.FunctionVersion != "1" || alias.AliasArn == "" || alias.RevisionID == "" {
		t.Fatalf("CreateAlias = %+v", alias)
	}

	c.call(http.MethodPost, fnPath+"aliased/aliases", aliasRequest{Name: "live", FunctionVersion: &version}, http.StatusConflict, nil)

	missing := "7"
	c.call(http.MethodPost, fnPath+"aliased/aliases", aliasRequest{Name: "next", FunctionVersion: &missing}, http.StatusNotFound, nil)
	c.call(http.MethodPost, fnPath+"aliased/aliases", aliasRequest{Name: "12", FunctionVersion: &version}, http.StatusBadRequest, nil)

	// The alias qualifies the function name like a version does.
	if _, err := c.svc.storage.GetFunction(t.Context(), "aliased:live"); err != nil {
		t.Errorf("GetFunction(aliased:live): %v", err)
	}

	if _, err := c.svc.storage.GetFunction(t.Context(), "aliased:nope"); err == nil {
		t.Error("GetFunction(aliased:nope) must fail")
	}

	// Versions referenced by an alias cannot be deleted.
	c.call(http.MethodDelete, fnPath+"aliased?Qualifier=1", nil, http.StatusConflict, nil)

	latest := latestVersion
	c.call(http.MethodPut, fnPath+"aliased/aliases/live", aliasRequest{FunctionVersion: &latest, RevisionID: "stale"}, http.StatusPreconditionFailed, nil)

	var updated aliasConfiguration

	c.call(http.MethodPut, fnPath+"aliased/aliases/live", aliasRequest{FunctionVersion: &latest, RevisionID: alias.RevisionID}, http.StatusOK, &updated)

	if updated.FunctionVersion != latestVersion || updated.Description != description || updated.RevisionID == alias.RevisionID {
		t.Errorf("UpdateAlias = %+v", updated)
	}

	var got aliasConfiguration

	c.call(http.MethodGet, fnPath+"aliased/aliases/live", nil, http.StatusOK, &got)

	var list listAliasesResponse

	c.call(http.MethodGet, fnPath+"aliased/aliases", nil, http.StatusOK, &list)

	if got.FunctionVersion != latestVersion || len(list.Aliases) != 1 {
		t.Errorf("GetAlias = %+v, ListAliases = %+v", got, list)
	}

	c.call(http.MethodDelete, fnPath+"aliased/aliases/live", nil, http.StatusNoContent, nil)
	c.call(http.MethodGet, fnPath+"aliased/aliases/live", nil, http.StatusNotFound, nil)
}
