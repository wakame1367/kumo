package apigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Patch operation verbs (JSON Patch subset API Gateway accepts).
const (
	patchAdd     = "add"
	patchRemove  = "remove"
	patchReplace = "replace"
	patchMove    = "move"
	patchCopy    = "copy"
	patchTest    = "test"
)

// patchSegments splits a patch path into its JSON-pointer segments,
// unescaping "~1" to "/" and "~0" to "~".
func patchSegments(path string) []string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}

	return parts
}

func invalidPatch(op PatchOperation) error {
	return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid patch path '%s' specified for op '%s'.", op.Path, op.Op)}
}

// applyPatch runs each operation through apply, which receives the
// operation and its decoded path segments. move / copy / test are rejected:
// none of the patched resources accept them for the paths kumo models.
func applyPatch(ops []PatchOperation, apply func(op PatchOperation, segs []string) error) error {
	for _, op := range ops {
		switch op.Op {
		case patchAdd, patchRemove, patchReplace:
		case patchMove, patchCopy, patchTest:
			return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid patch operation specified. Must be 'add'|'remove'|'replace', was '%s'.", op.Op)}
		default:
			return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid patch operation specified: '%s'.", op.Op)}
		}

		if err := apply(op, patchSegments(op.Path)); err != nil {
			return err
		}
	}

	return nil
}

// patchString sets or clears a string field.
func patchString(field *string, op PatchOperation) {
	if op.Op == patchRemove {
		*field = ""

		return
	}

	*field = op.Value
}

// patchBool sets a boolean field; remove resets it to false.
func patchBool(field *bool, op PatchOperation) error {
	if op.Op == patchRemove {
		*field = false

		return nil
	}

	v, err := strconv.ParseBool(op.Value)
	if err != nil {
		return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid boolean value '%s' for path '%s'.", op.Value, op.Path)}
	}

	*field = v

	return nil
}

// patchInt32 sets an integer field; remove resets it to zero.
func patchInt32(field *int32, op PatchOperation) error {
	if op.Op == patchRemove {
		*field = 0

		return nil
	}

	v, err := strconv.ParseInt(op.Value, 10, 32)
	if err != nil {
		return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid integer value '%s' for path '%s'.", op.Value, op.Path)}
	}

	*field = int32(v)

	return nil
}

// patchStringMap adds, replaces or removes one key of a string map.
func patchStringMap(m *map[string]string, op PatchOperation, key string) {
	if op.Op == patchRemove {
		delete(*m, key)

		return
	}

	if *m == nil {
		*m = make(map[string]string)
	}

	(*m)[key] = op.Value
}

// patchBoolMap adds, replaces or removes one key of a boolean map. An add
// without a value (as the console sends for request parameters) means false.
func patchBoolMap(m *map[string]bool, op PatchOperation, key string) error {
	if op.Op == patchRemove {
		delete(*m, key)

		return nil
	}

	v := false

	if op.Value != "" {
		parsed, err := strconv.ParseBool(op.Value)
		if err != nil {
			return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid boolean value '%s' for path '%s'.", op.Value, op.Path)}
		}

		v = parsed
	}

	if *m == nil {
		*m = make(map[string]bool)
	}

	(*m)[key] = v

	return nil
}

// patchStringList adds or removes one element of a string list (the path's
// last segment names the element, as for binaryMediaTypes/{type}).
func patchStringList(list *[]string, op PatchOperation, value string) {
	*list = slices.DeleteFunc(*list, func(v string) bool { return v == value })

	if op.Op != patchRemove {
		*list = append(*list, value)
	}
}

// UpdateRestAPI applies patch operations to a REST API.
func (s *MemoryStorage) UpdateRestAPI(_ context.Context, restAPIID string, ops []PatchOperation) (*RestAPI, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, exists := s.RestAPIs[restAPIID]
	if !exists {
		return nil, &ServiceError{Code: errRestAPINotFound, Message: msgInvalidRestAPIID}
	}

	api := data.API.clone()

	err := applyPatch(ops, func(op PatchOperation, segs []string) error {
		switch {
		case len(segs) == 1 && segs[0] == "name":
			if op.Op == patchRemove || op.Value == "" {
				return &ServiceError{Code: errBadRequest, Message: "Name is required"}
			}

			api.Name = op.Value
		case len(segs) == 1 && segs[0] == "description":
			patchString(&api.Description, op)
		case len(segs) == 1 && segs[0] == "version":
			patchString(&api.Version, op)
		case len(segs) == 1 && segs[0] == "apiKeySource":
			patchString(&api.APIKeySource, op)
		case len(segs) == 1 && segs[0] == "policy":
			patchString(&api.Policy, op)
		case len(segs) == 1 && segs[0] == "disableExecuteApiEndpoint":
			return patchBool(&api.DisableExecuteAPIEndpt, op)
		case len(segs) == 1 && segs[0] == "minimumCompressionSize":
			return patchMinimumCompressionSize(api, op)
		case len(segs) == 2 && segs[0] == "binaryMediaTypes":
			patchStringList(&api.BinaryMediaTypes, op, segs[1])
		case len(segs) >= 2 && segs[0] == "endpointConfiguration":
			return patchEndpointConfiguration(api, op, segs[1:])
		default:
			return invalidPatch(op)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	data.API = api

	s.saveLocked()

	return api.clone(), nil
}

func patchMinimumCompressionSize(api *RestAPI, op PatchOperation) error {
	if op.Op == patchRemove || op.Value == "" {
		api.MinimumCompressionSize = nil

		return nil
	}

	var v int32
	if err := patchInt32(&v, op); err != nil {
		return err
	}

	api.MinimumCompressionSize = &v

	return nil
}

// patchEndpointConfiguration handles /endpointConfiguration/types/{index or
// type} and /endpointConfiguration/vpcEndpointIds/{id}.
func patchEndpointConfiguration(api *RestAPI, op PatchOperation, segs []string) error {
	if api.EndpointConfiguration == nil {
		api.EndpointConfiguration = &EndpointConfig{}
	}

	cfg := api.EndpointConfiguration

	switch {
	case len(segs) == 2 && segs[0] == "types":
		// replace /endpointConfiguration/types/0 with the new type: an API
		// has exactly one endpoint type.
		if op.Op == patchReplace {
			cfg.Types = []string{op.Value}

			return nil
		}

		patchStringList(&cfg.Types, op, segs[1])
	case len(segs) == 2 && segs[0] == "vpcEndpointIds":
		patchStringList(&cfg.VpcEndpointIDs, op, segs[1])
	case len(segs) == 1 && segs[0] == "ipAddressType":
		patchString(&cfg.IPAddressType, op)
	default:
		return invalidPatch(op)
	}

	return nil
}

// UpdateResource applies patch operations to a resource. Moving a resource
// (/parentId) or renaming it (/pathPart) recomputes the paths of the
// resource and its descendants.
func (s *MemoryStorage) UpdateResource(_ context.Context, restAPIID, resourceID string, ops []PatchOperation) (*Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, exists := s.RestAPIs[restAPIID]
	if !exists {
		return nil, &ServiceError{Code: errRestAPINotFound, Message: msgInvalidRestAPIID}
	}

	resource, exists := data.Resources[resourceID]
	if !exists {
		return nil, &ServiceError{Code: errResourceNotFound, Message: msgInvalidResourceID}
	}

	if resource.Path == "/" {
		return nil, &ServiceError{Code: errBadRequest, Message: "Cannot update the root resource"}
	}

	parentID, pathPart := resource.ParentID, resource.PathPart

	err := applyPatch(ops, func(op PatchOperation, segs []string) error {
		if len(segs) != 1 || op.Op != patchReplace {
			return invalidPatch(op)
		}

		switch segs[0] {
		case "parentId":
			if _, ok := data.Resources[op.Value]; !ok {
				return &ServiceError{Code: errResourceNotFound, Message: msgInvalidResourceID}
			}

			parentID = op.Value
		case "pathPart":
			pathPart = op.Value
		default:
			return invalidPatch(op)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	for id := parentID; id != ""; id = data.Resources[id].ParentID {
		if id == resourceID {
			return nil, &ServiceError{Code: errBadRequest, Message: "A resource cannot be moved under itself"}
		}
	}

	resource.ParentID, resource.PathPart = parentID, pathPart
	data.repathLocked(resource)

	s.saveLocked()

	return resource.clone(), nil
}

// repathLocked recomputes the path of resource and of every descendant.
func (d *RestAPIData) repathLocked(resource *Resource) {
	resource.Path = buildPath(d.Resources[resource.ParentID].Path, resource.PathPart)

	for _, child := range d.Resources {
		if child.ParentID == resource.ID {
			d.repathLocked(child)
		}
	}
}

// UpdateMethod applies patch operations to a method.
func (s *MemoryStorage) UpdateMethod(_ context.Context, restAPIID, resourceID, httpMethod string, ops []PatchOperation) (*Method, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resource, method, err := s.methodLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	updated := method.clone()

	err = applyPatch(ops, func(op PatchOperation, segs []string) error {
		switch {
		case len(segs) == 1 && segs[0] == "authorizationType":
			patchString(&updated.AuthorizationType, op)
		case len(segs) == 1 && segs[0] == "authorizerId":
			patchString(&updated.AuthorizerID, op)
		case len(segs) == 1 && segs[0] == "apiKeyRequired":
			return patchBool(&updated.APIKeyRequired, op)
		case len(segs) == 1 && segs[0] == "operationName":
			patchString(&updated.OperationName, op)
		case len(segs) == 1 && segs[0] == "requestValidatorId":
			patchString(&updated.RequestValidatorID, op)
		case len(segs) == 2 && segs[0] == "requestParameters":
			return patchBoolMap(&updated.RequestParameters, op, segs[1])
		case len(segs) == 2 && segs[0] == "requestModels":
			patchStringMap(&updated.RequestModels, op, segs[1])
		case len(segs) == 2 && segs[0] == "authorizationScopes":
			patchStringList(&updated.AuthorizationScopes, op, segs[1])
		default:
			return invalidPatch(op)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	resource.ResourceMethods[httpMethod] = *updated

	s.saveLocked()

	return updated.clone(), nil
}

// UpdateIntegration applies patch operations to a method's integration.
func (s *MemoryStorage) UpdateIntegration(_ context.Context, restAPIID, resourceID, httpMethod string, ops []PatchOperation) (*Integration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	resource, method, err := s.integrationLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	updated := method.MethodIntegration.clone()

	err = applyPatch(ops, func(op PatchOperation, segs []string) error {
		switch {
		case len(segs) == 1 && segs[0] == "type":
			patchString(&updated.Type, op)
		case len(segs) == 1 && segs[0] == "httpMethod":
			patchString(&updated.HTTPMethod, op)
		case len(segs) == 1 && segs[0] == "uri":
			patchString(&updated.URI, op)
		case len(segs) == 1 && segs[0] == "connectionType":
			patchString(&updated.ConnectionType, op)
		case len(segs) == 1 && segs[0] == "connectionId":
			patchString(&updated.ConnectionID, op)
		case len(segs) == 1 && segs[0] == "credentials":
			patchString(&updated.Credentials, op)
		case len(segs) == 1 && segs[0] == "passthroughBehavior":
			patchString(&updated.PassthroughBehavior, op)
		case len(segs) == 1 && segs[0] == "contentHandling":
			patchString(&updated.ContentHandling, op)
		case len(segs) == 1 && segs[0] == "cacheNamespace":
			patchString(&updated.CacheNamespace, op)
		case len(segs) == 1 && segs[0] == "timeoutInMillis":
			return patchInt32(&updated.TimeoutInMillis, op)
		case len(segs) == 2 && segs[0] == "requestParameters":
			patchStringMap(&updated.RequestParameters, op, segs[1])
		case len(segs) == 2 && segs[0] == "requestTemplates":
			patchStringMap(&updated.RequestTemplates, op, segs[1])
		case len(segs) == 2 && segs[0] == "cacheKeyParameters":
			patchStringList(&updated.CacheKeyParameters, op, segs[1])
		default:
			return invalidPatch(op)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	method.MethodIntegration = updated
	resource.ResourceMethods[httpMethod] = method

	s.saveLocked()

	return updated.clone(), nil
}

// UpdateMethodResponse applies patch operations to a method response.
func (s *MemoryStorage) UpdateMethodResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string, ops []PatchOperation) (*MethodResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, method, err := s.methodLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	current, exists := method.MethodResponses[statusCode]
	if !exists {
		return nil, &ServiceError{Code: errMethodNotFound, Message: msgInvalidStatusCode}
	}

	updated := current.clone()

	err = applyPatch(ops, func(op PatchOperation, segs []string) error {
		switch {
		case len(segs) == 2 && segs[0] == "responseParameters":
			return patchBoolMap(&updated.ResponseParameters, op, segs[1])
		case len(segs) == 2 && segs[0] == "responseModels":
			patchStringMap(&updated.ResponseModels, op, segs[1])
		default:
			return invalidPatch(op)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	method.MethodResponses[statusCode] = updated

	s.saveLocked()

	return updated.clone(), nil
}

// UpdateIntegrationResponse applies patch operations to an integration response.
func (s *MemoryStorage) UpdateIntegrationResponse(_ context.Context, restAPIID, resourceID, httpMethod, statusCode string, ops []PatchOperation) (*IntegrationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, method, err := s.integrationLocked(restAPIID, resourceID, httpMethod)
	if err != nil {
		return nil, err
	}

	current, exists := method.MethodIntegration.IntegrationResponses[statusCode]
	if !exists {
		return nil, &ServiceError{Code: errMethodNotFound, Message: msgInvalidStatusCode}
	}

	updated := current.clone()

	err = applyPatch(ops, func(op PatchOperation, segs []string) error {
		switch {
		case len(segs) == 1 && segs[0] == "selectionPattern":
			patchString(&updated.SelectionPattern, op)
		case len(segs) == 1 && segs[0] == "contentHandling":
			patchString(&updated.ContentHandling, op)
		case len(segs) == 2 && segs[0] == "responseParameters":
			patchStringMap(&updated.ResponseParameters, op, segs[1])
		case len(segs) == 2 && segs[0] == "responseTemplates":
			patchStringMap(&updated.ResponseTemplates, op, segs[1])
		default:
			return invalidPatch(op)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	method.MethodIntegration.IntegrationResponses[statusCode] = updated

	s.saveLocked()

	return updated.clone(), nil
}

// UpdateDeployment applies patch operations to a deployment.
func (s *MemoryStorage) UpdateDeployment(_ context.Context, restAPIID, deploymentID string, ops []PatchOperation) (*Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, exists := s.RestAPIs[restAPIID]
	if !exists {
		return nil, &ServiceError{Code: errRestAPINotFound, Message: msgInvalidRestAPIID}
	}

	deployment, exists := data.Deployments[deploymentID]
	if !exists {
		return nil, &ServiceError{Code: errDeploymentNotFound, Message: msgInvalidDeploymentID}
	}

	updated := *deployment

	err := applyPatch(ops, func(op PatchOperation, segs []string) error {
		if len(segs) != 1 || segs[0] != "description" {
			return invalidPatch(op)
		}

		patchString(&updated.Description, op)

		return nil
	})
	if err != nil {
		return nil, err
	}

	*deployment = updated

	s.saveLocked()

	return &updated, nil
}

// UpdateStage applies patch operations to a stage: its deployment,
// description, variables, tracing, access log settings, cache cluster and
// per-method settings (/{resourcePath}/{httpMethod}/{group}/{setting}).
func (s *MemoryStorage) UpdateStage(_ context.Context, restAPIID, stageName string, ops []PatchOperation) (*Stage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, exists := s.RestAPIs[restAPIID]
	if !exists {
		return nil, &ServiceError{Code: errRestAPINotFound, Message: msgInvalidRestAPIID}
	}

	stage, exists := data.Stages[stageName]
	if !exists {
		return nil, &ServiceError{Code: errStageNotFound, Message: "Invalid stage identifier specified"}
	}

	updated := stage.clone()

	err := applyPatch(ops, func(op PatchOperation, segs []string) error {
		switch {
		case len(segs) == 1 && segs[0] == "deploymentId":
			if _, ok := data.Deployments[op.Value]; !ok {
				return &ServiceError{Code: errDeploymentNotFound, Message: msgInvalidDeploymentID}
			}

			updated.DeploymentID = op.Value
		case len(segs) == 1 && segs[0] == "description":
			patchString(&updated.Description, op)
		case len(segs) == 1 && segs[0] == "cacheClusterEnabled":
			return patchBool(&updated.CacheClusterEnabled, op)
		case len(segs) == 1 && segs[0] == "cacheClusterSize":
			patchString(&updated.CacheClusterSize, op)
		case len(segs) == 1 && segs[0] == "tracingEnabled":
			return patchBool(&updated.TracingEnabled, op)
		case len(segs) == 2 && segs[0] == "variables":
			patchStringMap(&updated.Variables, op, segs[1])
		case segs[0] == "accessLogSettings":
			return patchAccessLogSettings(updated, op, segs[1:])
		case len(segs) == 4:
			return patchMethodSetting(updated, op, segs)
		case len(segs) == 1 && (segs[0] == "clientCertificateId" || segs[0] == "documentationVersion" || segs[0] == "webAclArn"):
			// Accepted for compatibility; kumo does not model these.
		default:
			return invalidPatch(op)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	updated.LastUpdatedDate = time.Now()
	data.Stages[stageName] = updated

	s.saveLocked()

	return updated.clone(), nil
}

// patchAccessLogSettings handles /accessLogSettings (remove) and
// /accessLogSettings/{destinationArn,format}.
func patchAccessLogSettings(stage *Stage, op PatchOperation, segs []string) error {
	if len(segs) == 0 {
		if op.Op != patchRemove {
			return invalidPatch(op)
		}

		stage.AccessLogSettings = nil

		return nil
	}

	if len(segs) != 1 {
		return invalidPatch(op)
	}

	if stage.AccessLogSettings == nil {
		stage.AccessLogSettings = &AccessLogSettings{}
	}

	switch segs[0] {
	case "destinationArn":
		patchString(&stage.AccessLogSettings.DestinationArn, op)
	case "format":
		patchString(&stage.AccessLogSettings.Format, op)
	default:
		return invalidPatch(op)
	}

	if *stage.AccessLogSettings == (AccessLogSettings{}) {
		stage.AccessLogSettings = nil
	}

	return nil
}

// patchMethodSetting handles /{resourcePath}/{httpMethod}/{group}/{setting},
// e.g. /*/*/metrics/enabled or /~1items/GET/throttling/burstLimit. A remove
// of any setting drops the whole method settings block, as AWS does.
func patchMethodSetting(stage *Stage, op PatchOperation, segs []string) error {
	key := strings.TrimPrefix(segs[0], "/") + "/" + segs[1]

	if op.Op == patchRemove {
		delete(stage.MethodSettings, key)

		return nil
	}

	if stage.MethodSettings == nil {
		stage.MethodSettings = make(map[string]*MethodSetting)
	}

	setting, ok := stage.MethodSettings[key]
	if !ok {
		setting = &MethodSetting{ThrottlingBurstLimit: -1, ThrottlingRateLimit: -1, CacheTTLInSeconds: 300, LoggingLevel: "OFF", UnauthorizedCacheControlHeaderStrategy: "SUCCEED_WITH_RESPONSE_HEADER"}
		stage.MethodSettings[key] = setting
	}

	switch segs[2] + "/" + segs[3] {
	case "metrics/enabled":
		return patchBool(&setting.MetricsEnabled, op)
	case "logging/loglevel":
		patchString(&setting.LoggingLevel, op)
	case "logging/dataTrace":
		return patchBool(&setting.DataTraceEnabled, op)
	case "throttling/burstLimit":
		return patchInt32(&setting.ThrottlingBurstLimit, op)
	case "throttling/rateLimit":
		v, err := strconv.ParseFloat(op.Value, 64)
		if err != nil {
			return invalidPatch(op)
		}

		setting.ThrottlingRateLimit = v
	case "caching/enabled":
		return patchBool(&setting.CachingEnabled, op)
	case "caching/ttlInSeconds":
		return patchInt32(&setting.CacheTTLInSeconds, op)
	case "caching/dataEncrypted":
		return patchBool(&setting.CacheDataEncrypted, op)
	case "caching/requireAuthorizationForCacheControl":
		return patchBool(&setting.RequireAuthorizationForCacheControl, op)
	case "caching/unauthorizedCacheControlHeaderStrategy":
		patchString(&setting.UnauthorizedCacheControlHeaderStrategy, op)
	default:
		return invalidPatch(op)
	}

	return nil
}

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

	for k, m := range r.ResourceMethods {
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
