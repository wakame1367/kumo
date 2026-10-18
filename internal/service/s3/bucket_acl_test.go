package s3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type s3TestRouter struct{ mux *http.ServeMux }

func (m s3TestRouter) Handle(method, pattern string, h http.HandlerFunc) {
	m.mux.HandleFunc(method+" "+pattern, h)
}

func (m s3TestRouter) HandleFunc(method, pattern string, h http.HandlerFunc) {
	m.Handle(method, pattern, h)
}

// newRoutedS3 returns a function that sends a request through the S3 routes.
func newRoutedS3(t *testing.T) func(method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	svc := New(NewMemoryStorage(), defaultBaseURL)
	mux := http.NewServeMux()
	svc.RegisterRoutes(s3TestRouter{mux})

	return func(method, target, body string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}

		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		return w
	}
}

func TestBucketOwnershipControls(t *testing.T) {
	t.Parallel()

	do := newRoutedS3(t)

	if w := do(http.MethodPut, "/owned", "", nil); w.Code != http.StatusOK {
		t.Fatalf("CreateBucket: %d %s", w.Code, w.Body.String())
	}

	if w := do(http.MethodGet, "/owned?ownershipControls", "", nil); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), errCodeOwnershipControlsNotFound) {
		t.Errorf("unset ownership controls: %d %s", w.Code, w.Body.String())
	}

	body := `<OwnershipControls xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ObjectOwnership>BucketOwnerEnforced</ObjectOwnership></Rule></OwnershipControls>`
	if w := do(http.MethodPut, "/owned?ownershipControls", body, nil); w.Code != http.StatusOK {
		t.Fatalf("PutBucketOwnershipControls: %d %s", w.Code, w.Body.String())
	}

	if w := do(http.MethodGet, "/owned?ownershipControls", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<ObjectOwnership>BucketOwnerEnforced</ObjectOwnership>") {
		t.Errorf("GetBucketOwnershipControls: %d %s", w.Code, w.Body.String())
	}

	// ACLs are disabled: only owner-only ACLs are accepted.
	if w := do(http.MethodPut, "/owned?acl", "", map[string]string{"X-Amz-Acl": "public-read"}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errCodeACLNotSupported) {
		t.Errorf("public-read with ACLs disabled: %d %s", w.Code, w.Body.String())
	}

	if w := do(http.MethodPut, "/owned?acl", "", map[string]string{"X-Amz-Acl": "private"}); w.Code != http.StatusOK {
		t.Errorf("private with ACLs disabled: %d %s", w.Code, w.Body.String())
	}

	bad := `<OwnershipControls><Rule><ObjectOwnership>Nope</ObjectOwnership></Rule></OwnershipControls>`
	if w := do(http.MethodPut, "/owned?ownershipControls", bad, nil); w.Code != http.StatusBadRequest {
		t.Errorf("invalid ownership: %d", w.Code)
	}

	if w := do(http.MethodDelete, "/owned?ownershipControls", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DeleteBucketOwnershipControls: %d %s", w.Code, w.Body.String())
	}

	if w := do(http.MethodGet, "/owned?ownershipControls", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("after delete: %d", w.Code)
	}

	if w := do(http.MethodHead, "/owned", "", nil); w.Code != http.StatusOK {
		t.Errorf("bucket must survive ownership-controls delete: %d", w.Code)
	}
}

func TestBucketACL_CannedRoundTrip(t *testing.T) {
	t.Parallel()

	do := newRoutedS3(t)
	do(http.MethodPut, "/acl-bucket", "", nil)

	if w := do(http.MethodGet, "/acl-bucket?acl", "", nil); w.Code != http.StatusOK || strings.Contains(w.Body.String(), groupAllUsers) {
		t.Errorf("default ACL: %d %s", w.Code, w.Body.String())
	}

	if w := do(http.MethodPut, "/acl-bucket?acl", "", map[string]string{"X-Amz-Acl": "public-read"}); w.Code != http.StatusOK {
		t.Fatalf("PutBucketAcl: %d %s", w.Code, w.Body.String())
	}

	w := do(http.MethodGet, "/acl-bucket?acl", "", nil)
	if got := w.Body.String(); !strings.Contains(got, "<Permission>FULL_CONTROL</Permission>") || !strings.Contains(got, "<URI>"+groupAllUsers+"</URI>") {
		t.Errorf("public-read ACL = %s", got)
	}

	if w := do(http.MethodPut, "/missing?acl", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("ACL on a missing bucket: %d", w.Code)
	}
}

// TestDeleteBucketCors_KeepsBucket guards the regression where DELETE
// ?cors fell through to DeleteBucket.
func TestDeleteBucketCors_KeepsBucket(t *testing.T) {
	t.Parallel()

	do := newRoutedS3(t)
	do(http.MethodPut, "/cors-bucket", "", nil)

	cors := `<CORSConfiguration><CORSRule><AllowedMethod>GET</AllowedMethod><AllowedOrigin>*</AllowedOrigin></CORSRule></CORSConfiguration>`
	if w := do(http.MethodPut, "/cors-bucket?cors", cors, nil); w.Code != http.StatusOK {
		t.Fatalf("PutBucketCors: %d %s", w.Code, w.Body.String())
	}

	if w := do(http.MethodDelete, "/cors-bucket?cors", "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DeleteBucketCors: %d %s", w.Code, w.Body.String())
	}

	if w := do(http.MethodGet, "/cors-bucket?cors", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("CORS after delete: %d", w.Code)
	}

	if w := do(http.MethodHead, "/cors-bucket", "", nil); w.Code != http.StatusOK {
		t.Errorf("bucket must survive DeleteBucketCors: %d", w.Code)
	}
}
