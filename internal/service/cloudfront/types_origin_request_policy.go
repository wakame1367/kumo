package cloudfront

import (
	"encoding/xml"
	"time"
)

// Origin request policy error codes (CloudFront API reference).
const (
	errNoSuchOriginRequestPolicy        = "NoSuchOriginRequestPolicy"
	errOriginRequestPolicyAlreadyExists = "OriginRequestPolicyAlreadyExists"
	errOriginRequestPolicyInUse         = "OriginRequestPolicyInUse"
)

// Origin request policy header behaviors beyond the ones shared with cache
// policies (none, whitelist, allExcept).
const (
	originRequestHeaderAllViewer           = "allViewer"
	originRequestHeaderAllViewerAndCFFront = "allViewerAndWhitelistCloudFront"
)

// OriginRequestPolicy is a CloudFront origin request policy: the viewer
// headers, cookies and query strings the cache behaviors referencing it by
// OriginRequestPolicyId forward to the origin.
type OriginRequestPolicy struct {
	ID               string
	ETag             string
	LastModifiedTime time.Time
	Config           OriginRequestPolicyConfig
	// managed marks an AWS-managed policy; those are never stored.
	managed bool
}

// summaryType is the OriginRequestPolicySummary Type of the policy.
func (p *OriginRequestPolicy) summaryType() string {
	if p.managed {
		return cachePolicyTypeManaged
	}

	return cachePolicyTypeCustom
}

// OriginRequestPolicyConfig is the user-supplied configuration. It doubles as
// the request body of Create/UpdateOriginRequestPolicy and the response body
// of GetOriginRequestPolicyConfig.
type OriginRequestPolicyConfig struct {
	XMLName            xml.Name                          `xml:"OriginRequestPolicyConfig" json:"-"`
	Xmlns              string                            `xml:"xmlns,attr,omitempty" json:"-"`
	Comment            string                            `xml:"Comment,omitempty"`
	Name               string                            `xml:"Name"`
	HeadersConfig      *OriginRequestPolicyHeadersConfig `xml:"HeadersConfig"`
	CookiesConfig      *OriginRequestPolicyCookiesConfig `xml:"CookiesConfig"`
	QueryStringsConfig *OriginRequestPolicyQueryStrings  `xml:"QueryStringsConfig"`
}

// OriginRequestPolicyHeadersConfig selects the forwarded viewer headers.
type OriginRequestPolicyHeadersConfig struct {
	HeaderBehavior string            `xml:"HeaderBehavior"`
	Headers        *CachePolicyNames `xml:"Headers,omitempty"`
}

// OriginRequestPolicyCookiesConfig selects the forwarded cookies.
type OriginRequestPolicyCookiesConfig struct {
	CookieBehavior string            `xml:"CookieBehavior"`
	Cookies        *CachePolicyNames `xml:"Cookies,omitempty"`
}

// OriginRequestPolicyQueryStrings selects the forwarded query strings.
type OriginRequestPolicyQueryStrings struct {
	QueryStringBehavior string            `xml:"QueryStringBehavior"`
	QueryStrings        *CachePolicyNames `xml:"QueryStrings,omitempty"`
}

// OriginRequestPolicyXML is the OriginRequestPolicy element of
// Create/Get/UpdateOriginRequestPolicy and of an OriginRequestPolicySummary.
type OriginRequestPolicyXML struct {
	XMLName                   xml.Name                   `xml:"OriginRequestPolicy"`
	Xmlns                     string                     `xml:"xmlns,attr,omitempty"`
	ID                        string                     `xml:"Id"`
	LastModifiedTime          string                     `xml:"LastModifiedTime"`
	OriginRequestPolicyConfig *OriginRequestPolicyConfig `xml:"OriginRequestPolicyConfig"`
}

// OriginRequestPolicyListXML is the body of ListOriginRequestPolicies.
type OriginRequestPolicyListXML struct {
	XMLName    xml.Name                        `xml:"OriginRequestPolicyList"`
	Xmlns      string                          `xml:"xmlns,attr"`
	NextMarker string                          `xml:"NextMarker,omitempty"`
	MaxItems   int                             `xml:"MaxItems"`
	Quantity   int                             `xml:"Quantity"`
	Items      *OriginRequestPolicySummaryList `xml:"Items,omitempty"`
}

// OriginRequestPolicySummaryList is the Items element of OriginRequestPolicyList.
type OriginRequestPolicySummaryList struct {
	OriginRequestPolicySummary []OriginRequestPolicySummaryXML `xml:"OriginRequestPolicySummary"`
}

// OriginRequestPolicySummaryXML is one item of OriginRequestPolicyList.
type OriginRequestPolicySummaryXML struct {
	Type                string                  `xml:"Type"`
	OriginRequestPolicy *OriginRequestPolicyXML `xml:"OriginRequestPolicy"`
}
