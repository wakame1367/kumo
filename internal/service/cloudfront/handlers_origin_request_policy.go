package cloudfront

import (
	"net/http"
	"strconv"
	"time"
)

const originRequestPolicyPath = "/2020-05-31/origin-request-policy/"

// CreateOriginRequestPolicy handles POST /2020-05-31/origin-request-policy.
func (s *Service) CreateOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	cfg, ok := decodeXMLBody[OriginRequestPolicyConfig](w, r)
	if !ok {
		return
	}

	policy, err := s.storage.CreateOriginRequestPolicy(r.Context(), cfg)
	if err != nil {
		handleOriginRequestPolicyStorageError(w, err)

		return
	}

	w.Header().Set("ETag", policy.ETag)
	w.Header().Set("Location", originRequestPolicyPath+policy.ID)
	writeXMLResponse(w, http.StatusCreated, buildOriginRequestPolicyXML(policy, cloudfrontXmlns))
}

// GetOriginRequestPolicy handles GET /2020-05-31/origin-request-policy/{id}.
func (s *Service) GetOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	policy, err := s.storage.GetOriginRequestPolicy(r.Context(), r.PathValue("id"))
	if err != nil {
		handleOriginRequestPolicyStorageError(w, err)

		return
	}

	w.Header().Set("ETag", policy.ETag)
	writeXMLResponse(w, http.StatusOK, buildOriginRequestPolicyXML(policy, cloudfrontXmlns))
}

// GetOriginRequestPolicyConfig handles GET /2020-05-31/origin-request-policy/{id}/config.
func (s *Service) GetOriginRequestPolicyConfig(w http.ResponseWriter, r *http.Request) {
	policy, err := s.storage.GetOriginRequestPolicy(r.Context(), r.PathValue("id"))
	if err != nil {
		handleOriginRequestPolicyStorageError(w, err)

		return
	}

	cfg := policy.Config
	cfg.Xmlns = cloudfrontXmlns

	w.Header().Set("ETag", policy.ETag)
	writeXMLResponse(w, http.StatusOK, &cfg)
}

// UpdateOriginRequestPolicy handles PUT /2020-05-31/origin-request-policy/{id}.
func (s *Service) UpdateOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	cfg, ok := decodeXMLBody[OriginRequestPolicyConfig](w, r)
	if !ok {
		return
	}

	policy, err := s.storage.UpdateOriginRequestPolicy(r.Context(), r.PathValue("id"), cfg, r.Header.Get("If-Match"))
	if err != nil {
		handleOriginRequestPolicyStorageError(w, err)

		return
	}

	w.Header().Set("ETag", policy.ETag)
	writeXMLResponse(w, http.StatusOK, buildOriginRequestPolicyXML(policy, cloudfrontXmlns))
}

// DeleteOriginRequestPolicy handles DELETE /2020-05-31/origin-request-policy/{id}.
func (s *Service) DeleteOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.storage.DeleteOriginRequestPolicy(r.Context(), r.PathValue("id"), r.Header.Get("If-Match")); err != nil {
		handleOriginRequestPolicyStorageError(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListOriginRequestPolicies handles GET /2020-05-31/origin-request-policy.
func (s *Service) ListOriginRequestPolicies(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	maxItems := cachePolicyListMaxItems

	if raw := query.Get("MaxItems"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeCloudFrontError(w, errInvalidArgument, "MaxItems must be a positive integer.", http.StatusBadRequest)

			return
		}

		maxItems = n
	}

	policyType := query.Get("Type")
	if policyType != "" && policyType != cachePolicyTypeCustom && policyType != cachePolicyTypeManaged {
		writeCloudFrontError(w, errInvalidArgument, "Type must be one of managed, custom.", http.StatusBadRequest)

		return
	}

	items, nextMarker, err := s.storage.ListOriginRequestPolicies(r.Context(), policyType, query.Get("Marker"), maxItems)
	if err != nil {
		handleOriginRequestPolicyStorageError(w, err)

		return
	}

	list := &OriginRequestPolicyListXML{
		Xmlns:      cloudfrontXmlns,
		NextMarker: nextMarker,
		MaxItems:   maxItems,
		Quantity:   len(items),
	}

	if len(items) > 0 {
		list.Items = &OriginRequestPolicySummaryList{}

		for _, policy := range items {
			list.Items.OriginRequestPolicySummary = append(list.Items.OriginRequestPolicySummary,
				OriginRequestPolicySummaryXML{Type: policy.summaryType(), OriginRequestPolicy: buildOriginRequestPolicyXML(policy, "")})
		}
	}

	writeXMLResponse(w, http.StatusOK, list)
}

func buildOriginRequestPolicyXML(policy *OriginRequestPolicy, xmlns string) *OriginRequestPolicyXML {
	cfg := policy.Config

	return &OriginRequestPolicyXML{
		Xmlns:                     xmlns,
		ID:                        policy.ID,
		LastModifiedTime:          policy.LastModifiedTime.Format(time.RFC3339),
		OriginRequestPolicyConfig: &cfg,
	}
}

func handleOriginRequestPolicyStorageError(w http.ResponseWriter, err error) {
	writePolicyError(w, err, errNoSuchOriginRequestPolicy, errOriginRequestPolicyAlreadyExists, errOriginRequestPolicyInUse)
}
