package lambda

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// lastModifiedFormat is the timestamp format of Lambda function configurations.
const lastModifiedFormat = "2006-01-02T15:04:05.000+0000"

// aliasNamePattern is the documented alias name format: it may not be a bare
// version number, so a qualifier is unambiguously a version or an alias.
var aliasNamePattern = regexp.MustCompile(`^[a-zA-Z0-9-_]{1,128}$`)

// FunctionVersion is an immutable snapshot of a function's code and
// configuration, taken by PublishVersion.
type FunctionVersion struct {
	Version       string
	Description   string
	Runtime       string
	Role          string
	Handler       string
	Timeout       int
	MemorySize    int
	CodeSize      int64
	CodeSha256    string
	PackageType   string
	Architectures []string
	Environment   *Environment
	LastModified  time.Time
}

// Alias is a named pointer to a function version.
type Alias struct {
	Name            string
	FunctionVersion string
	Description     string
	RoutingConfig   *AliasRoutingConfig
	RevisionID      string
}

// AliasRoutingConfig shifts a share of an alias's traffic to another version.
// kumo stores it; invocations always run the function's handler.
type AliasRoutingConfig struct {
	AdditionalVersionWeights map[string]float64 `json:"AdditionalVersionWeights,omitempty"`
}

// publishVersionRequest is the body of PublishVersion.
type publishVersionRequest struct {
	CodeSha256  string `json:"CodeSha256,omitempty"`
	Description string `json:"Description,omitempty"`
	RevisionID  string `json:"RevisionId,omitempty"`
}

// aliasRequest is the body of CreateAlias and UpdateAlias; absent fields keep
// their value on update.
type aliasRequest struct {
	Name            string              `json:"Name,omitempty"`
	FunctionVersion *string             `json:"FunctionVersion,omitempty"`
	Description     *string             `json:"Description,omitempty"`
	RoutingConfig   *AliasRoutingConfig `json:"RoutingConfig,omitempty"`
	RevisionID      string              `json:"RevisionId,omitempty"`
}

// aliasConfiguration is the wire shape of an alias.
type aliasConfiguration struct {
	AliasArn        string              `json:"AliasArn"`
	Name            string              `json:"Name"`
	FunctionVersion string              `json:"FunctionVersion"`
	Description     string              `json:"Description,omitempty"`
	RoutingConfig   *AliasRoutingConfig `json:"RoutingConfig,omitempty"`
	RevisionID      string              `json:"RevisionId"`
}

// listAliasesResponse is the wire shape of ListAliases.
type listAliasesResponse struct {
	Aliases    []aliasConfiguration `json:"Aliases"`
	NextMarker string               `json:"NextMarker,omitempty"`
}

// listVersionsByFunctionResponse is the wire shape of ListVersionsByFunction.
type listVersionsByFunctionResponse struct {
	Versions   []*FunctionConfiguration `json:"Versions"`
	NextMarker string                   `json:"NextMarker,omitempty"`
}

// snapshot captures the function's current code and configuration as a
// version (without a version number).
func (fn *Function) snapshot() FunctionVersion {
	return FunctionVersion{
		Description:   fn.Description,
		Runtime:       fn.Runtime,
		Role:          fn.Role,
		Handler:       fn.Handler,
		Timeout:       fn.Timeout,
		MemorySize:    fn.MemorySize,
		CodeSize:      fn.CodeSize,
		CodeSha256:    fn.CodeSha256,
		PackageType:   fn.PackageType,
		Architectures: slices.Clone(fn.Architectures),
		Environment:   cloneEnvironment(fn.Environment),
		LastModified:  fn.LastModified,
	}
}

// sameAs reports whether the version was published from the same code and
// configuration as other; PublishVersion returns the existing version then.
func (v *FunctionVersion) sameAs(other *FunctionVersion) bool {
	return v.CodeSha256 == other.CodeSha256 && v.Runtime == other.Runtime && v.Role == other.Role &&
		v.Handler == other.Handler && v.Timeout == other.Timeout && v.MemorySize == other.MemorySize &&
		v.PackageType == other.PackageType && slices.Equal(v.Architectures, other.Architectures) &&
		maps.Equal(v.Environment.variables(), other.Environment.variables())
}

func (e *Environment) variables() map[string]string {
	if e == nil {
		return nil
	}

	return e.Variables
}

func cloneEnvironment(e *Environment) *Environment {
	if e == nil {
		return nil
	}

	return &Environment{Variables: maps.Clone(e.Variables)}
}

func (v *FunctionVersion) clone() *FunctionVersion {
	out := *v
	out.Architectures = slices.Clone(v.Architectures)
	out.Environment = cloneEnvironment(v.Environment)

	return &out
}

func (a *Alias) clone() *Alias {
	out := *a

	if a.RoutingConfig != nil {
		out.RoutingConfig = &AliasRoutingConfig{AdditionalVersionWeights: maps.Clone(a.RoutingConfig.AdditionalVersionWeights)}
	}

	return &out
}

// versionLocked returns the published version with the given number.
func (fn *Function) versionLocked(version string) (*FunctionVersion, bool) {
	for _, v := range fn.Versions {
		if v.Version == version {
			return v, true
		}
	}

	return nil, false
}

// hasQualifier reports whether qualifier names $LATEST, a published version or
// an alias of the function.
func (fn *Function) hasQualifier(qualifier string) bool {
	if qualifier == "" || qualifier == latestVersion {
		return true
	}

	if _, ok := fn.versionLocked(qualifier); ok {
		return true
	}

	_, ok := fn.Aliases[qualifier]

	return ok
}

// publishLocked publishes the function's current state as a new version, or
// returns the latest version when nothing changed since it was published.
func (s *MemoryStorage) publishLocked(fn *Function, description string) *FunctionVersion {
	snap := fn.snapshot()

	if n := len(fn.Versions); n > 0 && fn.Versions[n-1].sameAs(&snap) {
		return fn.Versions[n-1]
	}

	fn.LastVersion++
	snap.Version = strconv.Itoa(fn.LastVersion)
	snap.LastModified = time.Now().UTC()

	if description != "" {
		snap.Description = description
	}

	fn.Versions = append(fn.Versions, &snap)

	return &snap
}

// PublishVersion publishes the function's current code and configuration as
// a new version. A CodeSha256 that does not match the current code is a
// PreconditionFailed, as on AWS.
func (s *MemoryStorage) PublishVersion(_ context.Context, functionName, codeSha256, description string) (*Function, *FunctionVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, exists := s.lookupLocked(functionName)
	if !exists {
		return nil, nil, functionNotFound(functionName)
	}

	if codeSha256 != "" && codeSha256 != fn.CodeSha256 {
		return nil, nil, &FunctionError{Type: ErrPreconditionFailed, Message: "CodeSHA256 (" + codeSha256 + ") is different from current CodeSHA256 in $LATEST. Please try again with the CodeSHA256 in $LATEST."}
	}

	version := s.publishLocked(fn, description)

	s.saveLocked()

	return fn, version.clone(), nil
}

// ListVersionsByFunction returns the function and its published versions
// in publication order.
func (s *MemoryStorage) ListVersionsByFunction(_ context.Context, functionName string) (*Function, []*FunctionVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fn, exists := s.lookupLocked(functionName)
	if !exists {
		return nil, nil, functionNotFound(functionName)
	}

	versions := make([]*FunctionVersion, len(fn.Versions))
	for i, v := range fn.Versions {
		versions[i] = v.clone()
	}

	return fn, versions, nil
}

// GetFunctionVersion resolves a qualifier ($LATEST, a version number or an
// alias) of the function: the returned version is nil for $LATEST.
func (s *MemoryStorage) GetFunctionVersion(_ context.Context, functionName, qualifier string) (*Function, *FunctionVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fn, exists := s.lookupLocked(functionName)
	if !exists {
		return nil, nil, functionNotFound(functionName)
	}

	if alias, ok := fn.Aliases[qualifier]; ok {
		qualifier = alias.FunctionVersion
	}

	if qualifier == "" || qualifier == latestVersion {
		return fn, nil, nil
	}

	version, ok := fn.versionLocked(qualifier)
	if !ok {
		return nil, nil, functionNotFound(fn.FunctionArn + ":" + qualifier)
	}

	return fn, version.clone(), nil
}

// DeleteFunctionVersion deletes one published version. A version an alias
// points to cannot be deleted.
func (s *MemoryStorage) DeleteFunctionVersion(_ context.Context, functionName, version string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, exists := s.lookupLocked(functionName)
	if !exists {
		return functionNotFound(functionName)
	}

	if _, ok := fn.versionLocked(version); !ok {
		return functionNotFound(fn.FunctionArn + ":" + version)
	}

	for _, alias := range fn.Aliases {
		if alias.FunctionVersion == version {
			return &FunctionError{Type: ErrResourceConflict, Message: fmt.Sprintf("Unable to delete version because the following aliases reference it: [%s]", alias.Name)}
		}
	}

	fn.Versions = slices.DeleteFunc(fn.Versions, func(v *FunctionVersion) bool { return v.Version == version })

	s.saveLocked()

	return nil
}

// CreateAlias creates an alias pointing at $LATEST or a published version.
func (s *MemoryStorage) CreateAlias(_ context.Context, functionName string, req *aliasRequest) (*Function, *Alias, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, exists := s.lookupLocked(functionName)
	if !exists {
		return nil, nil, functionNotFound(functionName)
	}

	if !aliasNamePattern.MatchString(req.Name) || isVersionNumber(req.Name) {
		return nil, nil, &FunctionError{Type: ErrInvalidParameterValue, Message: "Alias name must match [a-zA-Z0-9-_]+ and must not be a version number."}
	}

	if _, dup := fn.Aliases[req.Name]; dup {
		return nil, nil, &FunctionError{Type: ErrResourceConflict, Message: "Alias already exists: " + fn.FunctionArn + ":" + req.Name}
	}

	alias := &Alias{Name: req.Name, RevisionID: uuid.NewString()}
	if err := applyAliasRequest(fn, alias, req, true); err != nil {
		return nil, nil, err
	}

	if fn.Aliases == nil {
		fn.Aliases = make(map[string]*Alias)
	}

	fn.Aliases[alias.Name] = alias

	s.saveLocked()

	return fn, alias.clone(), nil
}

// GetAlias returns an alias.
func (s *MemoryStorage) GetAlias(_ context.Context, functionName, name string) (*Function, *Alias, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fn, alias, err := s.aliasLocked(functionName, name)
	if err != nil {
		return nil, nil, err
	}

	return fn, alias.clone(), nil
}

// ListAliases returns the function's aliases sorted by name.
func (s *MemoryStorage) ListAliases(_ context.Context, functionName string) (*Function, []*Alias, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fn, exists := s.lookupLocked(functionName)
	if !exists {
		return nil, nil, functionNotFound(functionName)
	}

	aliases := make([]*Alias, 0, len(fn.Aliases))
	for _, name := range slices.Sorted(maps.Keys(fn.Aliases)) {
		aliases = append(aliases, fn.Aliases[name].clone())
	}

	return fn, aliases, nil
}

// UpdateAlias changes an alias's version, description or routing. A
// RevisionId that is not the alias's current one is a PreconditionFailed.
func (s *MemoryStorage) UpdateAlias(_ context.Context, functionName, name string, req *aliasRequest) (*Function, *Alias, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, alias, err := s.aliasLocked(functionName, name)
	if err != nil {
		return nil, nil, err
	}

	if req.RevisionID != "" && req.RevisionID != alias.RevisionID {
		return nil, nil, &FunctionError{Type: ErrPreconditionFailed, Message: "The Revision Id provided does not match the latest Revision Id. Call the GetAlias API to retrieve the latest Revision Id"}
	}

	updated := alias.clone()
	if err := applyAliasRequest(fn, updated, req, false); err != nil {
		return nil, nil, err
	}

	updated.RevisionID = uuid.NewString()
	fn.Aliases[name] = updated

	s.saveLocked()

	return fn, updated.clone(), nil
}

// DeleteAlias deletes an alias.
func (s *MemoryStorage) DeleteAlias(_ context.Context, functionName, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, _, err := s.aliasLocked(functionName, name)
	if err != nil {
		return err
	}

	delete(fn.Aliases, name)

	s.saveLocked()

	return nil
}

func (s *MemoryStorage) aliasLocked(functionName, name string) (*Function, *Alias, error) {
	fn, exists := s.lookupLocked(functionName)
	if !exists {
		return nil, nil, functionNotFound(functionName)
	}

	alias, ok := fn.Aliases[name]
	if !ok {
		return nil, nil, &FunctionError{Type: ErrResourceNotFound, Message: "Alias not found: " + fn.FunctionArn + ":" + name}
	}

	return fn, alias, nil
}

// applyAliasRequest validates and applies the alias fields of a request. On
// create the FunctionVersion is mandatory.
func applyAliasRequest(fn *Function, alias *Alias, req *aliasRequest, create bool) error {
	if req.FunctionVersion != nil {
		alias.FunctionVersion = *req.FunctionVersion
	} else if create {
		return &FunctionError{Type: ErrInvalidParameterValue, Message: "FunctionVersion is required"}
	}

	if alias.FunctionVersion != latestVersion {
		if _, ok := fn.versionLocked(alias.FunctionVersion); !ok {
			return functionNotFound(fn.FunctionArn + ":" + alias.FunctionVersion)
		}
	}

	if req.Description != nil {
		alias.Description = *req.Description
	}

	if req.RoutingConfig != nil {
		for version := range req.RoutingConfig.AdditionalVersionWeights {
			if _, ok := fn.versionLocked(version); !ok {
				return functionNotFound(fn.FunctionArn + ":" + version)
			}
		}

		alias.RoutingConfig = &AliasRoutingConfig{AdditionalVersionWeights: maps.Clone(req.RoutingConfig.AdditionalVersionWeights)}
		if len(alias.RoutingConfig.AdditionalVersionWeights) == 0 {
			alias.RoutingConfig = nil
		}
	}

	return nil
}

func isVersionNumber(s string) bool {
	_, err := strconv.Atoi(s)

	return err == nil
}

func functionNotFound(name string) error {
	return &FunctionError{Type: ErrResourceNotFound, Message: "Function not found: " + name}
}

// versionToConfiguration renders a published version as a
// FunctionConfiguration, qualified by its version number.
func versionToConfiguration(fn *Function, v *FunctionVersion) *FunctionConfiguration {
	return &FunctionConfiguration{
		FunctionName:     fn.FunctionName,
		FunctionArn:      fn.FunctionArn + ":" + v.Version,
		Runtime:          v.Runtime,
		Role:             v.Role,
		Handler:          v.Handler,
		CodeSize:         v.CodeSize,
		Description:      v.Description,
		Timeout:          v.Timeout,
		MemorySize:       v.MemorySize,
		LastModified:     v.LastModified.Format(lastModifiedFormat),
		CodeSha256:       v.CodeSha256,
		Version:          v.Version,
		State:            fn.State,
		LastUpdateStatus: fn.LastUpdateStatus,
		PackageType:      v.PackageType,
		Architectures:    v.Architectures,
		Environment:      v.Environment,
	}
}

func toAliasConfiguration(fn *Function, a *Alias) aliasConfiguration {
	return aliasConfiguration{
		AliasArn:        fn.FunctionArn + ":" + a.Name,
		Name:            a.Name,
		FunctionVersion: a.FunctionVersion,
		Description:     a.Description,
		RoutingConfig:   a.RoutingConfig,
		RevisionID:      a.RevisionID,
	}
}

// PublishVersion handles POST /2015-03-31/functions/{functionName}/versions.
func (s *Service) PublishVersion(w http.ResponseWriter, r *http.Request) {
	var req publishVersionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeFunctionError(w, ErrInvalidParameterValue, "Invalid request body", http.StatusBadRequest)

			return
		}
	}

	fn, version, err := s.storage.PublishVersion(r.Context(), r.PathValue("functionName"), req.CodeSha256, req.Description)
	if err != nil {
		handleFunctionError(w, err)

		return
	}

	writeJSONResponse(w, http.StatusCreated, versionToConfiguration(fn, version))
}

// ListVersionsByFunction handles GET /2015-03-31/functions/{functionName}/versions:
// $LATEST first, then the published versions in publication order.
func (s *Service) ListVersionsByFunction(w http.ResponseWriter, r *http.Request) {
	fn, versions, err := s.storage.ListVersionsByFunction(r.Context(), r.PathValue("functionName"))
	if err != nil {
		handleFunctionError(w, err)

		return
	}

	latest := functionToConfiguration(fn)
	latest.FunctionArn = fn.FunctionArn + ":" + latestVersion
	latest.Version = latestVersion

	out := listVersionsByFunctionResponse{Versions: []*FunctionConfiguration{latest}}
	for _, v := range versions {
		out.Versions = append(out.Versions, versionToConfiguration(fn, v))
	}

	writeJSONResponse(w, http.StatusOK, out)
}

// CreateAlias handles POST /2015-03-31/functions/{functionName}/aliases.
func (s *Service) CreateAlias(w http.ResponseWriter, r *http.Request) {
	var req aliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeFunctionError(w, ErrInvalidParameterValue, "Invalid request body", http.StatusBadRequest)

		return
	}

	fn, alias, err := s.storage.CreateAlias(r.Context(), r.PathValue("functionName"), &req)
	if err != nil {
		handleFunctionError(w, err)

		return
	}

	writeJSONResponse(w, http.StatusCreated, toAliasConfiguration(fn, alias))
}

// GetAlias handles GET /2015-03-31/functions/{functionName}/aliases/{aliasName}.
func (s *Service) GetAlias(w http.ResponseWriter, r *http.Request) {
	fn, alias, err := s.storage.GetAlias(r.Context(), r.PathValue("functionName"), r.PathValue("aliasName"))
	if err != nil {
		handleFunctionError(w, err)

		return
	}

	writeJSONResponse(w, http.StatusOK, toAliasConfiguration(fn, alias))
}

// UpdateAlias handles PUT /2015-03-31/functions/{functionName}/aliases/{aliasName}.
func (s *Service) UpdateAlias(w http.ResponseWriter, r *http.Request) {
	var req aliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeFunctionError(w, ErrInvalidParameterValue, "Invalid request body", http.StatusBadRequest)

		return
	}

	fn, alias, err := s.storage.UpdateAlias(r.Context(), r.PathValue("functionName"), r.PathValue("aliasName"), &req)
	if err != nil {
		handleFunctionError(w, err)

		return
	}

	writeJSONResponse(w, http.StatusOK, toAliasConfiguration(fn, alias))
}

// DeleteAlias handles DELETE /2015-03-31/functions/{functionName}/aliases/{aliasName}.
func (s *Service) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	if err := s.storage.DeleteAlias(r.Context(), r.PathValue("functionName"), r.PathValue("aliasName")); err != nil {
		handleFunctionError(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListAliases handles GET /2015-03-31/functions/{functionName}/aliases.
func (s *Service) ListAliases(w http.ResponseWriter, r *http.Request) {
	fn, aliases, err := s.storage.ListAliases(r.Context(), r.PathValue("functionName"))
	if err != nil {
		handleFunctionError(w, err)

		return
	}

	out := listAliasesResponse{Aliases: make([]aliasConfiguration, 0, len(aliases))}

	version := r.URL.Query().Get("FunctionVersion")

	for _, a := range aliases {
		if version == "" || a.FunctionVersion == version {
			out.Aliases = append(out.Aliases, toAliasConfiguration(fn, a))
		}
	}

	writeJSONResponse(w, http.StatusOK, out)
}
