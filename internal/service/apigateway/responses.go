package apigateway

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"regexp"
)

const (
	msgInvalidStatusCode        = "Invalid Response status code specified"
	msgInvalidIntegrationID     = "Invalid Integration identifier specified"
	msgStatusCodeFormat         = "Status code must be a three-digit HTTP status code between 100 and 599"
	segmentResponses            = "responses"
	segmentIntegration          = "integration"
	methodResponsePathParts     = 7 // {api}/resources/{res}/methods/{m}/responses/{code}
	integrationResponsePathPart = 8 // {api}/resources/{res}/methods/{m}/integration/responses/{code}
)

// statusCodePattern is the documented method / integration response status
// code format.
var statusCodePattern = regexp.MustCompile(`^[1-5]\d\d$`)

// methodLocked resolves a method for mutation. The returned Method is a copy;
// callers write it back with resource.ResourceMethods[httpMethod] = method.
func (s *MemoryStorage) methodLocked(restAPIID, resourceID, httpMethod string) (*Resource, Method, error) {
	data, exists := s.RestAPIs[restAPIID]
	if !exists {
		return nil, Method{}, &ServiceError{Code: errRestAPINotFound, Message: msgInvalidRestAPIID}
	}

	resource, exists := data.Resources[resourceID]
	if !exists {
		return nil, Method{}, &ServiceError{Code: errResourceNotFound, Message: msgInvalidResourceID}
	}

	method, exists := resource.ResourceMethods[httpMethod]
	if !exists {
		return nil, Method{}, &ServiceError{Code: errMethodNotFound, Message: msgInvalidMethodID}
	}

	return resource, method, nil
}

// integrationLocked resolves a method's integration for mutation.
func (s *MemoryStorage) integrationLocked(restAPIID, resourceID, httpMethod string) (*Resource, Method, error) {
	resource, method, err := s.methodLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, Method{}, err
	}

	if method.MethodIntegration == nil {
		return nil, Method{}, &ServiceError{Code: errMethodNotFound, Message: msgInvalidIntegrationID}
	}

	return resource, method, nil
}

// PutMethodResponse creates or replaces a method response.
func (s *MemoryStorage) PutMethodResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string, req *PutMethodResponseRequest) (*MethodResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resource, method, err := s.methodLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	resp := &MethodResponse{StatusCode: statusCode, ResponseParameters: maps.Clone(req.ResponseParameters), ResponseModels: maps.Clone(req.ResponseModels)}

	if method.MethodResponses == nil {
		method.MethodResponses = make(map[string]*MethodResponse)
	}

	method.MethodResponses[statusCode] = resp
	resource.ResourceMethods[httpMethod] = method

	s.saveLocked()

	return resp.clone(), nil
}

// GetMethodResponse returns a method response.
func (s *MemoryStorage) GetMethodResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string) (*MethodResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, method, err := s.methodLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	resp, exists := method.MethodResponses[statusCode]
	if !exists {
		return nil, &ServiceError{Code: errMethodNotFound, Message: msgInvalidStatusCode}
	}

	return resp.clone(), nil
}

// DeleteMethodResponse removes a method response.
func (s *MemoryStorage) DeleteMethodResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, method, err := s.methodLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return err
	}

	if _, exists := method.MethodResponses[statusCode]; !exists {
		return &ServiceError{Code: errMethodNotFound, Message: msgInvalidStatusCode}
	}

	delete(method.MethodResponses, statusCode)

	s.saveLocked()

	return nil
}

// DeleteIntegration removes a method's integration, its integration
// responses included.
func (s *MemoryStorage) DeleteIntegration(_ context.Context, restAPIID, resourceID, httpMethod string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	resource, method, err := s.integrationLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return err
	}

	method.MethodIntegration = nil
	resource.ResourceMethods[httpMethod] = method

	s.saveLocked()

	return nil
}

// PutIntegrationResponse creates or replaces an integration response.
func (s *MemoryStorage) PutIntegrationResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string, req *PutIntegrationResponseRequest) (*IntegrationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, method, err := s.integrationLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	resp := &IntegrationResponse{
		StatusCode:         statusCode,
		SelectionPattern:   req.SelectionPattern,
		ResponseParameters: maps.Clone(req.ResponseParameters),
		ResponseTemplates:  maps.Clone(req.ResponseTemplates),
		ContentHandling:    req.ContentHandling,
	}

	integration := method.MethodIntegration
	if integration.IntegrationResponses == nil {
		integration.IntegrationResponses = make(map[string]*IntegrationResponse)
	}

	integration.IntegrationResponses[statusCode] = resp

	s.saveLocked()

	return resp.clone(), nil
}

// GetIntegrationResponse returns an integration response.
func (s *MemoryStorage) GetIntegrationResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string) (*IntegrationResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, method, err := s.integrationLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	resp, exists := method.MethodIntegration.IntegrationResponses[statusCode]
	if !exists {
		return nil, &ServiceError{Code: errMethodNotFound, Message: msgInvalidStatusCode}
	}

	return resp.clone(), nil
}

// DeleteIntegrationResponse removes an integration response.
func (s *MemoryStorage) DeleteIntegrationResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, method, err := s.integrationLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return err
	}

	if _, exists := method.MethodIntegration.IntegrationResponses[statusCode]; !exists {
		return &ServiceError{Code: errMethodNotFound, Message: msgInvalidStatusCode}
	}

	delete(method.MethodIntegration.IntegrationResponses, statusCode)

	s.saveLocked()

	return nil
}

func (r *MethodResponse) clone() *MethodResponse {
	out := *r
	out.ResponseParameters = maps.Clone(r.ResponseParameters)
	out.ResponseModels = maps.Clone(r.ResponseModels)

	return &out
}

func (r *IntegrationResponse) clone() *IntegrationResponse {
	out := *r
	out.ResponseParameters = maps.Clone(r.ResponseParameters)
	out.ResponseTemplates = maps.Clone(r.ResponseTemplates)

	return &out
}

// responsePath splits a method / integration response path into its ids,
// reporting false when the path does not have the expected shape.
func responsePath(path string, integration bool) (restAPIID, resourceID, httpMethod, statusCode string, ok bool) {
	parts := pathSegmentsAfterRestapis(path)

	want, responsesAt := methodResponsePathParts, 5
	if integration {
		want, responsesAt = integrationResponsePathPart, 6
	}

	if len(parts) != want || parts[responsesAt] != segmentResponses || (integration && parts[5] != segmentIntegration) {
		return "", "", "", "", false
	}

	return parts[0], parts[2], parts[4], parts[want-1], true
}

// responseIDs resolves the ids of a method / integration response request; on
// failure the error response has been written.
func responseIDs(w http.ResponseWriter, r *http.Request, integration bool) (restAPIID, resourceID, httpMethod, statusCode string, ok bool) {
	restAPIID, resourceID, httpMethod, statusCode, ok = responsePath(r.URL.Path, integration)
	if !ok {
		writeError(w, errBadRequest, "Invalid request path", http.StatusBadRequest)

		return "", "", "", "", false
	}

	if !statusCodePattern.MatchString(statusCode) {
		writeError(w, errBadRequest, msgStatusCodeFormat, http.StatusBadRequest)

		return "", "", "", "", false
	}

	return restAPIID, resourceID, httpMethod, statusCode, true
}

// decodeBody decodes a JSON request body; an empty body decodes to the zero
// value. On failure the error response has been written.
func decodeBody[T any](w http.ResponseWriter, r *http.Request) (*T, bool) {
	var v T
	if r.ContentLength == 0 {
		return &v, true
	}

	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeError(w, errBadRequest, "Invalid request body", http.StatusBadRequest)

		return nil, false
	}

	return &v, true
}

// PutMethodResponse handles the PutMethodResponse API.
func (s *Service) PutMethodResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, false)
	if !ok {
		return
	}

	req, ok := decodeBody[PutMethodResponseRequest](w, r)
	if !ok {
		return
	}

	resp, err := s.storage.PutMethodResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode, req)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, resp, http.StatusCreated)
}

// GetMethodResponse handles the GetMethodResponse API.
func (s *Service) GetMethodResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, false)
	if !ok {
		return
	}

	resp, err := s.storage.GetMethodResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, resp, http.StatusOK)
}

// DeleteMethodResponse handles the DeleteMethodResponse API.
func (s *Service) DeleteMethodResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, false)
	if !ok {
		return
	}

	if err := s.storage.DeleteMethodResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode); err != nil {
		handleError(w, err)

		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// DeleteIntegration handles the DeleteIntegration API.
func (s *Service) DeleteIntegration(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod := extractIntegrationParams(r.URL.Path)

	if err := s.storage.DeleteIntegration(r.Context(), restAPIID, resourceID, httpMethod); err != nil {
		handleError(w, err)

		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// PutIntegrationResponse handles the PutIntegrationResponse API.
func (s *Service) PutIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, true)
	if !ok {
		return
	}

	req, ok := decodeBody[PutIntegrationResponseRequest](w, r)
	if !ok {
		return
	}

	if req.SelectionPattern != "" {
		if _, err := regexp.Compile(req.SelectionPattern); err != nil {
			writeError(w, errBadRequest, "Invalid selection pattern specified: "+req.SelectionPattern, http.StatusBadRequest)

			return
		}
	}

	resp, err := s.storage.PutIntegrationResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode, req)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, resp, http.StatusCreated)
}

// GetIntegrationResponse handles the GetIntegrationResponse API.
func (s *Service) GetIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, true)
	if !ok {
		return
	}

	resp, err := s.storage.GetIntegrationResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, resp, http.StatusOK)
}

// DeleteIntegrationResponse handles the DeleteIntegrationResponse API.
func (s *Service) DeleteIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, true)
	if !ok {
		return
	}

	if err := s.storage.DeleteIntegrationResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode); err != nil {
		handleError(w, err)

		return
	}

	w.WriteHeader(http.StatusAccepted)
}
