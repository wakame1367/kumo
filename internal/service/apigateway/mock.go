package apigateway

import (
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	integrationTypeMock    = "MOCK"
	contentTypeJSON        = "application/json"
	methodResponseHeaderKV = "method.response.header."
	defaultMockStatus      = http.StatusOK
)

// mockStatusPattern extracts the statusCode a MOCK request template selects
// when the template is not plain JSON (e.g. it contains VTL directives).
var mockStatusPattern = regexp.MustCompile(`"statusCode"\s*:\s*(\d{3})`)

// serveMock answers a MOCK integration the way API Gateway does: the request
// template's statusCode selects the integration response (by SelectionPattern
// against the status, falling back to the default response), whose status
// code, static header mappings ('literal' values) and response template make
// the method response. VTL in templates is not evaluated: templates are
// returned verbatim.
func serveMock(w http.ResponseWriter, r *http.Request, integration *Integration) {
	status := mockRequestStatus(r, integration.RequestTemplates)

	resp, ok := selectIntegrationResponse(integration.IntegrationResponses, strconv.Itoa(status))
	if !ok {
		writeExecuteError(w, http.StatusInternalServerError, executeErrorMessage(http.StatusInternalServerError))

		return
	}

	code, err := strconv.Atoi(resp.StatusCode)
	if err != nil {
		writeExecuteError(w, http.StatusInternalServerError, executeErrorMessage(http.StatusInternalServerError))

		return
	}

	for param, value := range resp.ResponseParameters {
		header, ok := strings.CutPrefix(param, methodResponseHeaderKV)
		if !ok {
			continue
		}

		// Only static values can be mapped: a MOCK integration has no
		// integration response headers or body to reference.
		if literal, ok := staticMappingValue(value); ok {
			w.Header().Set(header, literal)
		}
	}

	contentType, body := responseTemplate(r, resp.ResponseTemplates)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("x-amzn-RequestId", uuid.New().String())
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// mockRequestStatus reads the statusCode the request template for the
// request's content type selects, defaulting to 200.
func mockRequestStatus(r *http.Request, templates map[string]string) int {
	template, ok := templates[requestContentType(r)]
	if !ok {
		template, ok = templates[contentTypeJSON]
	}

	if !ok {
		return defaultMockStatus
	}

	var parsed struct {
		StatusCode int `json:"statusCode"`
	}

	if err := json.Unmarshal([]byte(template), &parsed); err == nil && parsed.StatusCode != 0 {
		return parsed.StatusCode
	}

	if m := mockStatusPattern.FindStringSubmatch(template); m != nil {
		if code, err := strconv.Atoi(m[1]); err == nil {
			return code
		}
	}

	return defaultMockStatus
}

// selectIntegrationResponse picks the integration response whose
// SelectionPattern matches the whole status, falling back to the default
// response (the one without a pattern). Patterns are tried in status-code
// order so the choice is deterministic.
func selectIntegrationResponse(responses map[string]*IntegrationResponse, status string) (*IntegrationResponse, bool) {
	var fallback *IntegrationResponse

	for _, code := range slices.Sorted(maps.Keys(responses)) {
		resp := responses[code]

		if resp.SelectionPattern == "" {
			if fallback == nil {
				fallback = resp
			}

			continue
		}

		re, err := regexp.Compile("^(?:" + resp.SelectionPattern + ")$")
		if err == nil && re.MatchString(status) {
			return resp, true
		}
	}

	return fallback, fallback != nil
}

// staticMappingValue unwraps a 'literal' mapping expression.
func staticMappingValue(value string) (string, bool) {
	if len(value) >= 2 && strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return value[1 : len(value)-1], true
	}

	return "", false
}

// responseTemplate selects the response template for the request's Accept
// header (application/json by default) and returns its content type and
// body. Without templates the body is empty JSON.
func responseTemplate(r *http.Request, templates map[string]string) (string, string) {
	for _, accept := range strings.Split(r.Header.Get("Accept"), ",") {
		ct := strings.TrimSpace(strings.SplitN(accept, ";", 2)[0])
		if body, ok := templates[ct]; ok {
			return ct, body
		}
	}

	if body, ok := templates[contentTypeJSON]; ok {
		return contentTypeJSON, body
	}

	for _, ct := range slices.Sorted(maps.Keys(templates)) {
		return ct, templates[ct]
	}

	return contentTypeJSON, ""
}

// requestContentType is the request's media type, application/json when absent.
func requestContentType(r *http.Request) string {
	ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])
	if ct == "" {
		return contentTypeJSON
	}

	return ct
}
