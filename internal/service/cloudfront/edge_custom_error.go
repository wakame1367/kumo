package cloudfront

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// applyDefaultRootObject serves the distribution's DefaultRootObject for a
// request to the distribution root, as CloudFront does. Only the root is
// rewritten: CloudFront never appends the object to subdirectory paths.
func applyDefaultRootObject(r *http.Request, dist *Distribution) {
	if r.PathValue("path") != "" || dist.DistributionConfig == nil || dist.DistributionConfig.DefaultRootObject == "" {
		return
	}

	r.SetPathValue("path", strings.TrimPrefix(dist.DistributionConfig.DefaultRootObject, "/"))
}

// customErrorResponse returns the custom error response configured for an
// origin status code, if any.
func customErrorResponse(dist *Distribution, status int) (*CustomErrorResponseXML, bool) {
	if status < http.StatusBadRequest || dist.DistributionConfig == nil || dist.DistributionConfig.CustomErrorResponses == nil {
		return nil, false
	}

	for i, item := range dist.DistributionConfig.CustomErrorResponses.Items {
		if item.ErrorCode == status {
			return &dist.DistributionConfig.CustomErrorResponses.Items[i], true
		}
	}

	return nil, false
}

// applyCustomErrorResponse replaces an origin error response with the custom
// error page the distribution configures for its status code: the page at
// ResponsePagePath is fetched through the cache behavior its path selects
// and served with ResponseCode (the origin's status when ResponseCode is
// empty). The origin response is returned unchanged when no custom error
// response matches or the error page itself cannot be fetched, which is what
// CloudFront does when the error page is unavailable.
func (s *Service) applyCustomErrorResponse(r *http.Request, dist *Distribution, resp *originResponse) *originResponse {
	custom, ok := customErrorResponse(dist, resp.StatusCode)
	if !ok {
		return resp
	}

	status := resp.StatusCode

	if custom.ResponseCode != "" {
		code, err := strconv.Atoi(custom.ResponseCode)
		if err != nil {
			return resp
		}

		status = code
	}

	if custom.ResponsePagePath == "" {
		resp.StatusCode = status

		return resp
	}

	page, err := s.fetchErrorPage(r, dist, strings.TrimPrefix(custom.ResponsePagePath, "/"))
	if err != nil || page.StatusCode >= http.StatusBadRequest {
		return resp
	}

	page.StatusCode = status

	return page
}

// errNoErrorPageOrigin reports that the error page path selects no usable origin.
var errNoErrorPageOrigin = errors.New("no usable origin for the custom error page")

// fetchErrorPage GETs a custom error page from the origin of the behavior
// its path selects, signed like any other request to that origin.
func (s *Service) fetchErrorPage(r *http.Request, dist *Distribution, pagePath string) (*originResponse, error) {
	behavior := resolveBehavior(dist, pagePath)

	origin, ok := selectOrigin(dist, behavior)
	if !ok {
		return nil, errNoErrorPageOrigin
	}

	pageURL, ok := edgeOriginURL(&origin, pagePath, "")
	if !ok {
		return nil, errNoErrorPageOrigin
	}

	signing, err := s.resolveSigning(r.Context(), &origin, behavior)
	if err != nil && !errors.Is(err, errNotSigned) {
		return nil, err
	}

	req := r.Clone(r.Context())
	req.Method = http.MethodGet

	for _, h := range []string{"Range", "If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "Content-Length", "Content-Type"} {
		req.Header.Del(h)
	}

	return originRequest(upstreamTarget{url: pageURL, signing: signing}, req, http.NoBody)
}
