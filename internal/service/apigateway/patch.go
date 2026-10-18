package apigateway

import (
	"context"
	"fmt"
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

// Patch path segments shared by several resources.
const (
	fieldDescription        = "description"
	fieldRequestParameters  = "requestParameters"
	fieldResponseParameters = "responseParameters"
	fieldContentHandling    = "contentHandling"
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

func invalidPatchValue(op PatchOperation, kind string) error {
	return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid %s value '%s' for path '%s'.", kind, op.Value, op.Path)}
}

// patcher applies patch operations to a T. fields handles one-segment paths
// (/name), entries two-segment paths into a map or list (/variables/{key}),
// and fallback, when set, every other path.
type patcher[T any] struct {
	fields   map[string]func(*T, PatchOperation) error
	entries  map[string]func(*T, PatchOperation, string) error
	fallback func(*T, PatchOperation, []string) error
}

// apply runs every operation against target. move / copy / test are rejected:
// none of the patched resources accept them for the paths kumo models.
func (p *patcher[T]) apply(target *T, ops []PatchOperation) error {
	for _, op := range ops {
		switch op.Op {
		case patchAdd, patchRemove, patchReplace:
		case patchMove, patchCopy, patchTest:
			return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid patch operation specified. Must be 'add'|'remove'|'replace', was '%s'.", op.Op)}
		default:
			return &ServiceError{Code: errBadRequest, Message: fmt.Sprintf("Invalid patch operation specified: '%s'.", op.Op)}
		}

		if err := p.applyOne(target, op, patchSegments(op.Path)); err != nil {
			return err
		}
	}

	return nil
}

func (p *patcher[T]) applyOne(target *T, op PatchOperation, segs []string) error {
	if f, ok := p.fields[segs[0]]; ok && len(segs) == 1 {
		return f(target, op)
	}

	if f, ok := p.entries[segs[0]]; ok && len(segs) == 2 {
		return f(target, op, segs[1])
	}

	if p.fallback != nil {
		return p.fallback(target, op, segs)
	}

	return invalidPatch(op)
}

// stringField patches a string field; remove clears it.
func stringField[T any](field func(*T) *string) func(*T, PatchOperation) error {
	return func(t *T, op PatchOperation) error {
		patchString(field(t), op)

		return nil
	}
}

// boolField patches a boolean field; remove resets it to false.
func boolField[T any](field func(*T) *bool) func(*T, PatchOperation) error {
	return func(t *T, op PatchOperation) error { return patchBool(field(t), op) }
}

// int32Field patches an integer field; remove resets it to zero.
func int32Field[T any](field func(*T) *int32) func(*T, PatchOperation) error {
	return func(t *T, op PatchOperation) error { return patchInt32(field(t), op) }
}

// stringMapEntry patches one key of a string map.
func stringMapEntry[T any](field func(*T) *map[string]string) func(*T, PatchOperation, string) error {
	return func(t *T, op PatchOperation, key string) error {
		m := field(t)
		*m = patchStringMap(*m, op, key)

		return nil
	}
}

// boolMapEntry patches one key of a boolean map.
func boolMapEntry[T any](field func(*T) *map[string]bool) func(*T, PatchOperation, string) error {
	return func(t *T, op PatchOperation, key string) error {
		m := field(t)

		updated, err := patchBoolMap(*m, op, key)
		if err != nil {
			return err
		}

		*m = updated

		return nil
	}
}

// stringListEntry adds or removes one element of a string list.
func stringListEntry[T any](field func(*T) *[]string) func(*T, PatchOperation, string) error {
	return func(t *T, op PatchOperation, value string) error {
		patchStringList(field(t), op, value)

		return nil
	}
}

func patchString(field *string, op PatchOperation) {
	if op.Op == patchRemove {
		*field = ""

		return
	}

	*field = op.Value
}

func patchBool(field *bool, op PatchOperation) error {
	if op.Op == patchRemove {
		*field = false

		return nil
	}

	v, err := strconv.ParseBool(op.Value)
	if err != nil {
		return invalidPatchValue(op, "boolean")
	}

	*field = v

	return nil
}

func patchInt32(field *int32, op PatchOperation) error {
	if op.Op == patchRemove {
		*field = 0

		return nil
	}

	v, err := strconv.ParseInt(op.Value, 10, 32)
	if err != nil {
		return invalidPatchValue(op, "integer")
	}

	*field = int32(v)

	return nil
}

// patchStringMap adds, replaces or removes one key of a string map and
// returns the (possibly newly allocated) map.
func patchStringMap(m map[string]string, op PatchOperation, key string) map[string]string {
	if op.Op == patchRemove {
		delete(m, key)

		return m
	}

	if m == nil {
		m = make(map[string]string)
	}

	m[key] = op.Value

	return m
}

// patchBoolMap adds, replaces or removes one key of a boolean map. An add
// without a value (as the console sends for request parameters) means false.
func patchBoolMap(m map[string]bool, op PatchOperation, key string) (map[string]bool, error) {
	if op.Op == patchRemove {
		delete(m, key)

		return m, nil
	}

	v := false

	if op.Value != "" {
		parsed, err := strconv.ParseBool(op.Value)
		if err != nil {
			return nil, invalidPatchValue(op, "boolean")
		}

		v = parsed
	}

	if m == nil {
		m = make(map[string]bool)
	}

	m[key] = v

	return m, nil
}

// patchStringList adds or removes one element of a string list (the path's
// last segment names the element, as for binaryMediaTypes/{type}).
func patchStringList(list *[]string, op PatchOperation, value string) {
	*list = slices.DeleteFunc(*list, func(v string) bool { return v == value })

	if op.Op != patchRemove {
		*list = append(*list, value)
	}
}

var restAPIPatcher = &patcher[RestAPI]{
	fields: map[string]func(*RestAPI, PatchOperation) error{
		"name": func(a *RestAPI, op PatchOperation) error {
			if op.Op == patchRemove || op.Value == "" {
				return &ServiceError{Code: errBadRequest, Message: "Name is required"}
			}

			a.Name = op.Value

			return nil
		},
		fieldDescription:            stringField(func(a *RestAPI) *string { return &a.Description }),
		"version":                   stringField(func(a *RestAPI) *string { return &a.Version }),
		"apiKeySource":              stringField(func(a *RestAPI) *string { return &a.APIKeySource }),
		"policy":                    stringField(func(a *RestAPI) *string { return &a.Policy }),
		"disableExecuteApiEndpoint": boolField(func(a *RestAPI) *bool { return &a.DisableExecuteAPIEndpt }),
		"minimumCompressionSize":    patchMinimumCompressionSize,
	},
	entries: map[string]func(*RestAPI, PatchOperation, string) error{
		"binaryMediaTypes": stringListEntry(func(a *RestAPI) *[]string { return &a.BinaryMediaTypes }),
	},
	fallback: func(a *RestAPI, op PatchOperation, segs []string) error {
		if segs[0] != "endpointConfiguration" || len(segs) < 2 {
			return invalidPatch(op)
		}

		return patchEndpointConfiguration(a, op, segs[1:])
	},
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
// type}, /endpointConfiguration/vpcEndpointIds/{id} and
// /endpointConfiguration/ipAddressType.
func patchEndpointConfiguration(api *RestAPI, op PatchOperation, segs []string) error {
	if api.EndpointConfiguration == nil {
		api.EndpointConfiguration = &EndpointConfig{}
	}

	cfg := api.EndpointConfiguration

	switch {
	case len(segs) == 2 && segs[0] == "types" && op.Op == patchReplace:
		// An API has exactly one endpoint type, so replacing any element
		// replaces the type.
		cfg.Types = []string{op.Value}
	case len(segs) == 2 && segs[0] == "types":
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

var methodPatcher = &patcher[Method]{
	fields: map[string]func(*Method, PatchOperation) error{
		"authorizationType":  stringField(func(m *Method) *string { return &m.AuthorizationType }),
		"authorizerId":       stringField(func(m *Method) *string { return &m.AuthorizerID }),
		"apiKeyRequired":     boolField(func(m *Method) *bool { return &m.APIKeyRequired }),
		"operationName":      stringField(func(m *Method) *string { return &m.OperationName }),
		"requestValidatorId": stringField(func(m *Method) *string { return &m.RequestValidatorID }),
	},
	entries: map[string]func(*Method, PatchOperation, string) error{
		fieldRequestParameters: boolMapEntry(func(m *Method) *map[string]bool { return &m.RequestParameters }),
		"requestModels":        stringMapEntry(func(m *Method) *map[string]string { return &m.RequestModels }),
		"authorizationScopes":  stringListEntry(func(m *Method) *[]string { return &m.AuthorizationScopes }),
	},
}

var integrationPatcher = &patcher[Integration]{
	fields: map[string]func(*Integration, PatchOperation) error{
		"type":                stringField(func(i *Integration) *string { return &i.Type }),
		"httpMethod":          stringField(func(i *Integration) *string { return &i.HTTPMethod }),
		"uri":                 stringField(func(i *Integration) *string { return &i.URI }),
		"connectionType":      stringField(func(i *Integration) *string { return &i.ConnectionType }),
		"connectionId":        stringField(func(i *Integration) *string { return &i.ConnectionID }),
		"credentials":         stringField(func(i *Integration) *string { return &i.Credentials }),
		"passthroughBehavior": stringField(func(i *Integration) *string { return &i.PassthroughBehavior }),
		fieldContentHandling:  stringField(func(i *Integration) *string { return &i.ContentHandling }),
		"cacheNamespace":      stringField(func(i *Integration) *string { return &i.CacheNamespace }),
		"timeoutInMillis":     int32Field(func(i *Integration) *int32 { return &i.TimeoutInMillis }),
	},
	entries: map[string]func(*Integration, PatchOperation, string) error{
		fieldRequestParameters: stringMapEntry(func(i *Integration) *map[string]string { return &i.RequestParameters }),
		"requestTemplates":     stringMapEntry(func(i *Integration) *map[string]string { return &i.RequestTemplates }),
		"cacheKeyParameters":   stringListEntry(func(i *Integration) *[]string { return &i.CacheKeyParameters }),
	},
}

var methodResponsePatcher = &patcher[MethodResponse]{
	entries: map[string]func(*MethodResponse, PatchOperation, string) error{
		fieldResponseParameters: boolMapEntry(func(r *MethodResponse) *map[string]bool { return &r.ResponseParameters }),
		"responseModels":        stringMapEntry(func(r *MethodResponse) *map[string]string { return &r.ResponseModels }),
	},
}

var integrationResponsePatcher = &patcher[IntegrationResponse]{
	fields: map[string]func(*IntegrationResponse, PatchOperation) error{
		"selectionPattern":   stringField(func(r *IntegrationResponse) *string { return &r.SelectionPattern }),
		fieldContentHandling: stringField(func(r *IntegrationResponse) *string { return &r.ContentHandling }),
	},
	entries: map[string]func(*IntegrationResponse, PatchOperation, string) error{
		fieldResponseParameters: stringMapEntry(func(r *IntegrationResponse) *map[string]string { return &r.ResponseParameters }),
		"responseTemplates":     stringMapEntry(func(r *IntegrationResponse) *map[string]string { return &r.ResponseTemplates }),
	},
}

var deploymentPatcher = &patcher[Deployment]{
	fields: map[string]func(*Deployment, PatchOperation) error{
		fieldDescription: stringField(func(d *Deployment) *string { return &d.Description }),
	},
}

// stagePatcher covers a stage's deployment, description, variables,
// tracing, cache cluster, access log settings and per-method settings
// (/{resourcePath}/{httpMethod}/{group}/{setting}). The deploymentId must
// name a deployment of the REST API, which UpdateStage checks first.
var stagePatcher = &patcher[Stage]{
	fields: map[string]func(*Stage, PatchOperation) error{
		"deploymentId":        stringField(func(s *Stage) *string { return &s.DeploymentID }),
		fieldDescription:      stringField(func(s *Stage) *string { return &s.Description }),
		"cacheClusterEnabled": boolField(func(s *Stage) *bool { return &s.CacheClusterEnabled }),
		"cacheClusterSize":    stringField(func(s *Stage) *string { return &s.CacheClusterSize }),
		"tracingEnabled":      boolField(func(s *Stage) *bool { return &s.TracingEnabled }),
		// Accepted for compatibility; kumo does not model these.
		"clientCertificateId":  func(*Stage, PatchOperation) error { return nil },
		"documentationVersion": func(*Stage, PatchOperation) error { return nil },
		"webAclArn":            func(*Stage, PatchOperation) error { return nil },
	},
	entries: map[string]func(*Stage, PatchOperation, string) error{
		"variables": stringMapEntry(func(s *Stage) *map[string]string { return &s.Variables }),
	},
	fallback: func(s *Stage, op PatchOperation, segs []string) error {
		switch {
		case segs[0] == "accessLogSettings":
			return patchAccessLogSettings(s, op, segs[1:])
		case len(segs) == 4:
			return patchMethodSetting(s, op, segs)
		default:
			return invalidPatch(op)
		}
	},
}

// patchAccessLogSettings handles /accessLogSettings (remove) and
// /accessLogSettings/{destinationArn,format}.
func patchAccessLogSettings(stage *Stage, op PatchOperation, segs []string) error {
	if len(segs) == 0 && op.Op == patchRemove {
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

// methodSettingFields maps the {group}/{setting} tail of a method settings
// patch path to the field it sets.
var methodSettingFields = map[string]func(*MethodSetting, PatchOperation) error{
	"metrics/enabled":       boolField(func(m *MethodSetting) *bool { return &m.MetricsEnabled }),
	"logging/loglevel":      stringField(func(m *MethodSetting) *string { return &m.LoggingLevel }),
	"logging/dataTrace":     boolField(func(m *MethodSetting) *bool { return &m.DataTraceEnabled }),
	"caching/enabled":       boolField(func(m *MethodSetting) *bool { return &m.CachingEnabled }),
	"caching/ttlInSeconds":  int32Field(func(m *MethodSetting) *int32 { return &m.CacheTTLInSeconds }),
	"caching/dataEncrypted": boolField(func(m *MethodSetting) *bool { return &m.CacheDataEncrypted }),
	"caching/requireAuthorizationForCacheControl": boolField(func(m *MethodSetting) *bool {
		return &m.RequireAuthorizationForCacheControl
	}),
	"caching/unauthorizedCacheControlHeaderStrategy": stringField(func(m *MethodSetting) *string {
		return &m.UnauthorizedCacheControlHeaderStrategy
	}),
	"throttling/burstLimit": int32Field(func(m *MethodSetting) *int32 { return &m.ThrottlingBurstLimit }),
	"throttling/rateLimit": func(m *MethodSetting, op PatchOperation) error {
		v, err := strconv.ParseFloat(op.Value, 64)
		if err != nil {
			return invalidPatchValue(op, "number")
		}

		m.ThrottlingRateLimit = v

		return nil
	},
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

	set, ok := methodSettingFields[segs[2]+"/"+segs[3]]
	if !ok {
		return invalidPatch(op)
	}

	if stage.MethodSettings == nil {
		stage.MethodSettings = make(map[string]*MethodSetting)
	}

	setting, ok := stage.MethodSettings[key]
	if !ok {
		setting = &MethodSetting{ThrottlingBurstLimit: -1, ThrottlingRateLimit: -1, CacheTTLInSeconds: 300, LoggingLevel: "OFF", UnauthorizedCacheControlHeaderStrategy: "SUCCEED_WITH_RESPONSE_HEADER"}
		stage.MethodSettings[key] = setting
	}

	return set(setting, op)
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
	if err := restAPIPatcher.apply(api, ops); err != nil {
		return nil, err
	}

	data.API = api

	s.saveLocked()

	return api.clone(), nil
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

	target := Resource{ParentID: resource.ParentID, PathPart: resource.PathPart}
	resourcePatcher := &patcher[Resource]{fields: map[string]func(*Resource, PatchOperation) error{
		"parentId": func(r *Resource, op PatchOperation) error {
			if _, ok := data.Resources[op.Value]; !ok || op.Op != patchReplace {
				return &ServiceError{Code: errResourceNotFound, Message: msgInvalidResourceID}
			}

			r.ParentID = op.Value

			return nil
		},
		"pathPart": stringField(func(r *Resource) *string { return &r.PathPart }),
	}}

	if err := resourcePatcher.apply(&target, ops); err != nil {
		return nil, err
	}

	for id := target.ParentID; id != ""; id = data.Resources[id].ParentID {
		if id == resourceID {
			return nil, &ServiceError{Code: errBadRequest, Message: "A resource cannot be moved under itself"}
		}
	}

	resource.ParentID, resource.PathPart = target.ParentID, target.PathPart
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
	if err := methodPatcher.apply(updated, ops); err != nil {
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
	if err := integrationPatcher.apply(updated, ops); err != nil {
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
	if err := methodResponsePatcher.apply(updated, ops); err != nil {
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
	if err := integrationResponsePatcher.apply(updated, ops); err != nil {
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
	if err := deploymentPatcher.apply(&updated, ops); err != nil {
		return nil, err
	}

	*deployment = updated

	s.saveLocked()

	return &updated, nil
}

// UpdateStage applies patch operations to a stage. /deploymentId must name a
// deployment of the same REST API.
func (s *MemoryStorage) UpdateStage(_ context.Context, restAPIID, stageName string, ops []PatchOperation) (*Stage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, exists := s.RestAPIs[restAPIID]
	if !exists {
		return nil, &ServiceError{Code: errRestAPINotFound, Message: msgInvalidRestAPIID}
	}

	stage, exists := data.Stages[stageName]
	if !exists {
		return nil, &ServiceError{Code: errStageNotFound, Message: msgInvalidStageID}
	}

	updated := stage.clone()

	for _, op := range ops {
		if op.Path != "/deploymentId" {
			continue
		}

		if _, ok := data.Deployments[op.Value]; !ok {
			return nil, &ServiceError{Code: errDeploymentNotFound, Message: msgInvalidDeploymentID}
		}
	}

	if err := stagePatcher.apply(updated, ops); err != nil {
		return nil, err
	}

	updated.LastUpdatedDate = time.Now()
	data.Stages[stageName] = updated

	s.saveLocked()

	return updated.clone(), nil
}
