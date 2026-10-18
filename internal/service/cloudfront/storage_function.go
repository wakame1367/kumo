package cloudfront

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// functionNamePattern is the documented CloudFront Function name format.
var functionNamePattern = regexp.MustCompile(`^[a-zA-Z0-9-_]{1,64}$`)

// ensureFunctionInit lazily initialises the function map, which a snapshot
// written before the feature existed does not contain.
func (s *MemoryStorage) ensureFunctionInit() {
	if s.Functions == nil {
		s.Functions = make(map[string]*Function)
	}
}

// CreateFunction stores a new function in the DEVELOPMENT stage. Names are
// unique per account (FunctionAlreadyExists).
func (s *MemoryStorage) CreateFunction(_ context.Context, req *CreateFunctionRequest) (*Function, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureFunctionInit()

	if !functionNamePattern.MatchString(req.Name) {
		return nil, invalidArgument(fmt.Sprintf("Name must match %s and be at most %d characters.", functionNamePattern, functionMaxNameLength))
	}

	if _, exists := s.Functions[req.Name]; exists {
		return nil, &Error{Code: errFunctionAlreadyExists, Message: "A function with the same name already exists in this AWS account."}
	}

	stage, err := newFunctionStage(req.FunctionConfig, req.FunctionCode)
	if err != nil {
		return nil, err
	}

	fn := &Function{
		Name:        req.Name,
		ARN:         "arn:aws:cloudfront::000000000000:function/" + req.Name,
		CreatedTime: stage.LastModifiedTime,
		Development: *stage,
	}
	s.Functions[fn.Name] = fn

	s.saveLocked()

	return s.snapshotLocked(fn), nil
}

// GetFunction returns a copy of the function.
func (s *MemoryStorage) GetFunction(_ context.Context, name string) (*Function, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fn, err := s.functionLocked(name)
	if err != nil {
		return nil, err
	}

	return s.snapshotLocked(fn), nil
}

// ListFunctions returns the functions that have the given stage (every
// function has DEVELOPMENT; only published ones have LIVE), sorted by name,
// starting after marker and limited to maxItems.
func (s *MemoryStorage) ListFunctions(_ context.Context, stage, marker string, maxItems int) ([]*Function, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if maxItems <= 0 {
		maxItems = functionListMaxItems
	}

	all := make([]*Function, 0, len(s.Functions))

	for _, fn := range s.Functions {
		if fn.stage(stage) != nil {
			all = append(all, s.snapshotLocked(fn))
		}
	}

	page, nextMarker := pageByID(all, func(f *Function) string { return f.Name }, marker, maxItems)

	return page, nextMarker, nil
}

// UpdateFunction replaces the DEVELOPMENT stage when ifMatch is its current
// ETag. LIVE keeps running the published code until the next publish.
func (s *MemoryStorage) UpdateFunction(_ context.Context, name string, req *UpdateFunctionRequest, ifMatch string) (*Function, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, err := s.functionForWriteLocked(name, ifMatch)
	if err != nil {
		return nil, err
	}

	stage, err := newFunctionStage(req.FunctionConfig, req.FunctionCode)
	if err != nil {
		return nil, err
	}

	fn.Development = *stage

	s.saveLocked()

	return s.snapshotLocked(fn), nil
}

// PublishFunction copies the DEVELOPMENT stage to LIVE when ifMatch is the
// DEVELOPMENT ETag.
func (s *MemoryStorage) PublishFunction(_ context.Context, name, ifMatch string) (*Function, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, err := s.functionForWriteLocked(name, ifMatch)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()

	fn.Development.Published = true
	live := fn.Development
	live.Code = append([]byte(nil), fn.Development.Code...)
	live.ETag = generateETag()
	live.LastModifiedTime = now
	fn.Live = &live

	s.saveLocked()

	return s.snapshotLocked(fn), nil
}

// DeleteFunction removes the function when ifMatch is the DEVELOPMENT ETag
// and no cache behavior of any distribution is associated with it.
func (s *MemoryStorage) DeleteFunction(_ context.Context, name, ifMatch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn, err := s.functionForWriteLocked(name, ifMatch)
	if err != nil {
		return err
	}

	if s.functionAssociatedLocked(fn.ARN) {
		return &Error{Code: errFunctionInUse, Message: "Cannot delete function, it's in use by one or more distributions."}
	}

	delete(s.Functions, name)

	s.saveLocked()

	return nil
}

// snapshotLocked returns a copy of the function carrying whether a
// distribution is associated with it, which its Status depends on.
func (s *MemoryStorage) snapshotLocked(fn *Function) *Function {
	out := fn.clone()
	out.associated = s.functionAssociatedLocked(fn.ARN)

	return out
}

func (s *MemoryStorage) functionLocked(name string) (*Function, error) {
	fn, ok := s.Functions[name]
	if !ok {
		return nil, noSuchFunction()
	}

	return fn, nil
}

// functionForWriteLocked applies the If-Match protocol against the
// DEVELOPMENT stage ETag.
func (s *MemoryStorage) functionForWriteLocked(name, ifMatch string) (*Function, error) {
	if err := requireIfMatch(ifMatch); err != nil {
		return nil, err
	}

	fn, err := s.functionLocked(name)
	if err != nil {
		return nil, err
	}

	if err := checkIfMatch(fn.Development.ETag, ifMatch); err != nil {
		return nil, err
	}

	return fn, nil
}

// functionAssociatedLocked reports whether any cache behavior of any
// distribution has a function association with the given ARN.
func (s *MemoryStorage) functionAssociatedLocked(arn string) bool {
	_, ok := s.distributionReferencingLocked(func(b *DefaultCacheBehavior) bool {
		return slices.ContainsFunc(b.FunctionAssociations.items(), func(a FunctionAssociationXML) bool { return a.FunctionARN == arn })
	})

	return ok
}

// validateFunctionAssociationsLocked rejects a distribution associating a
// function that does not exist or has never been published, as CloudFront
// does.
func (s *MemoryStorage) validateFunctionAssociationsLocked(config *DistributionConfig) error {
	for _, b := range config.behaviors() {
		for _, assoc := range b.FunctionAssociations.items() {
			fn := s.functionByARNLocked(assoc.FunctionARN)
			if fn == nil {
				return noSuchFunction()
			}

			if fn.Live == nil {
				return &Error{Code: errInvalidFunctionAssoc, Message: fmt.Sprintf("The function %s must be published before it can be associated with a distribution.", fn.Name)}
			}
		}
	}

	return nil
}

func (s *MemoryStorage) functionByARNLocked(arn string) *Function {
	for _, fn := range s.Functions {
		if fn.ARN == arn {
			return fn
		}
	}

	return nil
}

// newFunctionStage validates the configuration and base64 code of a
// Create/UpdateFunction request and returns the DEVELOPMENT stage to store.
func newFunctionStage(cfg *FunctionConfigXML, encodedCode string) (*FunctionStage, error) {
	if cfg == nil {
		return nil, invalidArgument("FunctionConfig is required.")
	}

	if !slices.Contains(functionRuntimes, cfg.Runtime) {
		return nil, invalidArgument("Runtime must be one of cloudfront-js-1.0, cloudfront-js-2.0.")
	}

	code, err := base64.StdEncoding.DecodeString(encodedCode)
	if err != nil || len(code) == 0 {
		return nil, invalidArgument("FunctionCode must be non-empty base64-encoded function code.")
	}

	if len(code) > functionMaxCodeSize {
		return nil, &Error{Code: errFunctionSizeLimitExceeded, Message: fmt.Sprintf("The function code exceeds the maximum size of %d bytes.", functionMaxCodeSize)}
	}

	return &FunctionStage{
		Comment:          cfg.Comment,
		Runtime:          cfg.Runtime,
		Code:             code,
		ETag:             generateETag(),
		LastModifiedTime: time.Now().UTC(),
	}, nil
}

func noSuchFunction() error {
	return &Error{Code: errNoSuchFunctionExists, Message: "The function does not exist."}
}
