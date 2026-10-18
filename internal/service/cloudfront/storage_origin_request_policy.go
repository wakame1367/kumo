package cloudfront

import (
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ensureOriginRequestPolicyInit lazily initialises the origin request policy
// map, which a snapshot written before the feature existed does not contain.
func (s *MemoryStorage) ensureOriginRequestPolicyInit() {
	if s.OriginRequestPolicies == nil {
		s.OriginRequestPolicies = make(map[string]*OriginRequestPolicy)
	}
}

// CreateOriginRequestPolicy stores a new custom origin request policy. Names
// are unique per account (OriginRequestPolicyAlreadyExists).
func (s *MemoryStorage) CreateOriginRequestPolicy(_ context.Context, cfg *OriginRequestPolicyConfig) (*OriginRequestPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureOriginRequestPolicyInit()

	if err := validateOriginRequestPolicyConfig(cfg); err != nil {
		return nil, err
	}

	if err := s.originRequestPolicyNameAvailableLocked(cfg.Name, ""); err != nil {
		return nil, err
	}

	policy := &OriginRequestPolicy{ID: uuid.NewString(), ETag: generateETag(), LastModifiedTime: time.Now().UTC(), Config: *storedOriginRequestPolicyConfig(cfg)}
	s.OriginRequestPolicies[policy.ID] = policy

	s.saveLocked()

	return policy.clone(), nil
}

// GetOriginRequestPolicy returns a copy of the origin request policy,
// AWS-managed ones included.
func (s *MemoryStorage) GetOriginRequestPolicy(_ context.Context, id string) (*OriginRequestPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if managed, ok := managedOriginRequestPolicy(id); ok {
		return managed, nil
	}

	policy, ok := s.OriginRequestPolicies[id]
	if !ok {
		return nil, noSuchOriginRequestPolicy()
	}

	return policy.clone(), nil
}

// ListOriginRequestPolicies returns the origin request policies of the
// requested type (both when policyType is empty) sorted by id, starting after
// marker and limited to maxItems; nextMarker is the last id returned when
// more remain.
func (s *MemoryStorage) ListOriginRequestPolicies(_ context.Context, policyType, marker string, maxItems int) ([]*OriginRequestPolicy, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if maxItems <= 0 {
		maxItems = cachePolicyListMaxItems
	}

	all := make([]*OriginRequestPolicy, 0, len(s.OriginRequestPolicies)+len(managedOriginRequestPolicySpecs))

	if policyType != cachePolicyTypeCustom {
		all = append(all, listManagedOriginRequestPolicies()...)
	}

	if policyType != cachePolicyTypeManaged {
		for _, policy := range s.OriginRequestPolicies {
			all = append(all, policy.clone())
		}
	}

	page, nextMarker := pageByID(all, func(p *OriginRequestPolicy) string { return p.ID }, marker, maxItems)

	return page, nextMarker, nil
}

// UpdateOriginRequestPolicy replaces the configuration when ifMatch is the
// current ETag and the new name is not taken by another policy.
func (s *MemoryStorage) UpdateOriginRequestPolicy(_ context.Context, id string, cfg *OriginRequestPolicyConfig, ifMatch string) (*OriginRequestPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	policy, err := s.originRequestPolicyForWriteLocked(id, ifMatch, errIllegalUpdate)
	if err != nil {
		return nil, err
	}

	if err := validateOriginRequestPolicyConfig(cfg); err != nil {
		return nil, err
	}

	if err := s.originRequestPolicyNameAvailableLocked(cfg.Name, id); err != nil {
		return nil, err
	}

	policy.Config = *storedOriginRequestPolicyConfig(cfg)
	policy.ETag = generateETag()
	policy.LastModifiedTime = time.Now().UTC()

	s.saveLocked()

	return policy.clone(), nil
}

// DeleteOriginRequestPolicy removes the policy when ifMatch is the current
// ETag and no cache behavior of any distribution references it.
func (s *MemoryStorage) DeleteOriginRequestPolicy(_ context.Context, id, ifMatch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.originRequestPolicyForWriteLocked(id, ifMatch, errIllegalDelete); err != nil {
		return err
	}

	if distID, ok := s.distributionReferencingLocked(func(b *DefaultCacheBehavior) bool { return b.OriginRequestPolicyID == id }); ok {
		return &Error{Code: errOriginRequestPolicyInUse, Message: fmt.Sprintf("Cannot delete the origin request policy because it is attached to one or more cache behaviors of distribution %s.", distID)}
	}

	delete(s.OriginRequestPolicies, id)

	s.saveLocked()

	return nil
}

func (s *MemoryStorage) originRequestPolicyExistsLocked(id string) bool {
	if _, ok := s.OriginRequestPolicies[id]; ok {
		return true
	}

	_, managed := managedOriginRequestPolicies[id]

	return managed
}

// originRequestPolicyForWriteLocked rejects writes to AWS-managed policies
// and applies the If-Match protocol.
func (s *MemoryStorage) originRequestPolicyForWriteLocked(id, ifMatch, illegal string) (*OriginRequestPolicy, error) {
	if _, managed := managedOriginRequestPolicies[id]; managed {
		return nil, &Error{Code: illegal, Message: "AWS-managed origin request policies cannot be modified or deleted."}
	}

	if err := requireIfMatch(ifMatch); err != nil {
		return nil, err
	}

	policy, ok := s.OriginRequestPolicies[id]
	if !ok {
		return nil, noSuchOriginRequestPolicy()
	}

	if err := checkIfMatch(policy.ETag, ifMatch); err != nil {
		return nil, err
	}

	return policy, nil
}

func (s *MemoryStorage) originRequestPolicyNameAvailableLocked(name, exceptID string) error {
	for _, other := range s.OriginRequestPolicies {
		if other.ID != exceptID && other.Config.Name == name {
			return &Error{Code: errOriginRequestPolicyAlreadyExists, Message: "An origin request policy with this name already exists. You must provide a unique name."}
		}
	}

	return nil
}

func noSuchOriginRequestPolicy() error {
	return &Error{Code: errNoSuchOriginRequestPolicy, Message: "The origin request policy does not exist."}
}

// validateOriginRequestPolicyConfig enforces the required members, the
// documented value sets and Quantity / Items consistency.
func validateOriginRequestPolicyConfig(cfg *OriginRequestPolicyConfig) error {
	switch {
	case cfg.Name == "":
		return invalidArgument("Name is required.")
	case utf8.RuneCountInString(cfg.Comment) > cachePolicyCommentMaxLength:
		return invalidArgument(fmt.Sprintf("The comment cannot be longer than %d characters.", cachePolicyCommentMaxLength))
	case cfg.HeadersConfig == nil:
		return invalidArgument("HeadersConfig is required.")
	case cfg.CookiesConfig == nil:
		return invalidArgument("CookiesConfig is required.")
	case cfg.QueryStringsConfig == nil:
		return invalidArgument("QueryStringsConfig is required.")
	case !slices.Contains([]string{cachePolicyBehaviorNone, cachePolicyBehaviorWhitelist, cachePolicyBehaviorAllExcept, originRequestHeaderAllViewer, originRequestHeaderAllViewerAndCFFront}, cfg.HeadersConfig.HeaderBehavior):
		return invalidArgument("HeaderBehavior must be one of none, whitelist, allViewer, allViewerAndWhitelistCloudFront, allExcept.")
	case !isCachePolicyListBehavior(cfg.CookiesConfig.CookieBehavior):
		return invalidArgument("CookieBehavior must be one of none, whitelist, allExcept, all.")
	case !isCachePolicyListBehavior(cfg.QueryStringsConfig.QueryStringBehavior):
		return invalidArgument("QueryStringBehavior must be one of none, whitelist, allExcept, all.")
	}

	for _, names := range []*CachePolicyNames{cfg.HeadersConfig.Headers, cfg.CookiesConfig.Cookies, cfg.QueryStringsConfig.QueryStrings} {
		if err := validateCachePolicyNames(names); err != nil {
			return err
		}
	}

	return nil
}

// storedOriginRequestPolicyConfig returns the deep copy of a validated
// request configuration to store.
func storedOriginRequestPolicyConfig(cfg *OriginRequestPolicyConfig) *OriginRequestPolicyConfig {
	out := cfg.clone()
	out.XMLName = xml.Name{}
	out.Xmlns = ""

	return out
}

func (p *OriginRequestPolicy) clone() *OriginRequestPolicy {
	out := *p
	out.Config = *p.Config.clone()

	return &out
}

// clone deep-copies the configuration so neither callers nor later requests
// share pointers or slices with the stored policy.
func (c *OriginRequestPolicyConfig) clone() *OriginRequestPolicyConfig {
	out := *c

	if c.HeadersConfig != nil {
		h := *c.HeadersConfig
		h.Headers = h.Headers.clone()
		out.HeadersConfig = &h
	}

	if c.CookiesConfig != nil {
		ck := *c.CookiesConfig
		ck.Cookies = ck.Cookies.clone()
		out.CookiesConfig = &ck
	}

	if c.QueryStringsConfig != nil {
		q := *c.QueryStringsConfig
		q.QueryStrings = q.QueryStrings.clone()
		out.QueryStringsConfig = &q
	}

	return &out
}
