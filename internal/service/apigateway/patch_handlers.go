package apigateway

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
)

func (a *RestAPI) clone() *RestAPI {
	out := *a
	out.Tags = maps.Clone(a.Tags)
	out.BinaryMediaTypes = slices.Clone(a.BinaryMediaTypes)

	if a.EndpointConfiguration != nil {
		cfg := *a.EndpointConfiguration
		cfg.Types = slices.Clone(cfg.Types)
		cfg.VpcEndpointIDs = slices.Clone(cfg.VpcEndpointIDs)
		out.EndpointConfiguration = &cfg
	}

	if a.MinimumCompressionSize != nil {
		v := *a.MinimumCompressionSize
		out.MinimumCompressionSize = &v
	}

	return &out
}

func (r *Resource) clone() *Resource {
	out := *r
	out.ResourceMethods = make(map[string]Method, len(r.ResourceMethods))

	for k := range r.ResourceMethods {
		m := r.ResourceMethods[k]
		out.ResourceMethods[k] = *m.clone()
	}

	return &out
}

func (m *Method) clone() *Method {
	out := *m
	out.RequestParameters = maps.Clone(m.RequestParameters)
	out.RequestModels = maps.Clone(m.RequestModels)
	out.AuthorizationScopes = slices.Clone(m.AuthorizationScopes)

	if m.MethodResponses != nil {
		out.MethodResponses = make(map[string]*MethodResponse, len(m.MethodResponses))
		for k, r := range m.MethodResponses {
			out.MethodResponses[k] = r.clone()
		}
	}

	if m.MethodIntegration != nil {
		out.MethodIntegration = m.MethodIntegration.clone()
	}

	return &out
}

func (i *Integration) clone() *Integration {
	out := *i
	out.CacheKeyParameters = slices.Clone(i.CacheKeyParameters)
	out.RequestParameters = maps.Clone(i.RequestParameters)
	out.RequestTemplates = maps.Clone(i.RequestTemplates)

	if i.IntegrationResponses != nil {
		out.IntegrationResponses = make(map[string]*IntegrationResponse, len(i.IntegrationResponses))
		for k, r := range i.IntegrationResponses {
			out.IntegrationResponses[k] = r.clone()
		}
	}

	return &out
}

func (st *Stage) clone() *Stage {
	out := *st
	out.Tags = maps.Clone(st.Tags)
	out.Variables = maps.Clone(st.Variables)

	if st.AccessLogSettings != nil {
		a := *st.AccessLogSettings
		out.AccessLogSettings = &a
	}

	if st.MethodSettings != nil {
		out.MethodSettings = make(map[string]*MethodSetting, len(st.MethodSettings))

		for k, v := range st.MethodSettings {
			ms := *v
			out.MethodSettings[k] = &ms
		}
	}

	return &out
}

// readPatch decodes the patchOperations of an Update* request; on failure the
// error response has been written.
func readPatch(w http.ResponseWriter, r *http.Request) ([]PatchOperation, bool) {
	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, errBadRequest, "Invalid request body", http.StatusBadRequest)

		return nil, false
	}

	return req.PatchOperations, true
}

// UpdateRestAPI handles the UpdateRestApi API.
func (s *Service) UpdateRestAPI(w http.ResponseWriter, r *http.Request) {
	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	api, err := s.storage.UpdateRestAPI(r.Context(), extractDeploymentRestAPIID(r.URL.Path), ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, toRestAPIResponse(api), http.StatusOK)
}

// UpdateResource handles the UpdateResource API.
func (s *Service) UpdateResource(w http.ResponseWriter, r *http.Request) {
	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	restAPIID, resourceID := extractRestAPIAndResourceID(r.URL.Path)

	resource, err := s.storage.UpdateResource(r.Context(), restAPIID, resourceID, ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, toResourceResponse(resource), http.StatusOK)
}

// UpdateMethod handles the UpdateMethod API.
func (s *Service) UpdateMethod(w http.ResponseWriter, r *http.Request) {
	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	restAPIID, resourceID, httpMethod := extractMethodParams(r.URL.Path)

	method, err := s.storage.UpdateMethod(r.Context(), restAPIID, resourceID, httpMethod, ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, toMethodOutput(method), http.StatusOK)
}

// UpdateIntegration handles the UpdateIntegration API.
func (s *Service) UpdateIntegration(w http.ResponseWriter, r *http.Request) {
	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	restAPIID, resourceID, httpMethod := extractIntegrationParams(r.URL.Path)

	integration, err := s.storage.UpdateIntegration(r.Context(), restAPIID, resourceID, httpMethod, ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, toIntegrationOutput(integration), http.StatusOK)
}

// UpdateMethodResponse handles the UpdateMethodResponse API.
func (s *Service) UpdateMethodResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, false)
	if !ok {
		return
	}

	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	resp, err := s.storage.UpdateMethodResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode, ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, resp, http.StatusOK)
}

// UpdateIntegrationResponse handles the UpdateIntegrationResponse API.
func (s *Service) UpdateIntegrationResponse(w http.ResponseWriter, r *http.Request) {
	restAPIID, resourceID, httpMethod, statusCode, ok := responseIDs(w, r, true)
	if !ok {
		return
	}

	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	resp, err := s.storage.UpdateIntegrationResponse(r.Context(), restAPIID, resourceID, httpMethod, statusCode, ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, resp, http.StatusOK)
}

// UpdateDeployment handles the UpdateDeployment API.
func (s *Service) UpdateDeployment(w http.ResponseWriter, r *http.Request) {
	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	restAPIID, deploymentID := extractRestAPIAndDeploymentID(r.URL.Path)

	deployment, err := s.storage.UpdateDeployment(r.Context(), restAPIID, deploymentID, ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, toDeploymentResponse(deployment), http.StatusOK)
}

// UpdateStage handles the UpdateStage API.
func (s *Service) UpdateStage(w http.ResponseWriter, r *http.Request) {
	ops, ok := readPatch(w, r)
	if !ok {
		return
	}

	restAPIID, stageName := extractRestAPIAndStageName(r.URL.Path)

	stage, err := s.storage.UpdateStage(r.Context(), restAPIID, stageName, ops)
	if err != nil {
		handleError(w, err)

		return
	}

	writeResponse(w, toStageResponse(stage), http.StatusOK)
}
