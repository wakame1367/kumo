package cloudfront

import (
	"encoding/xml"
	"time"
)

// CloudFront Functions error codes (CloudFront API reference).
const (
	errFunctionAlreadyExists     = "FunctionAlreadyExists"
	errNoSuchFunctionExists      = "NoSuchFunctionExists"
	errFunctionInUse             = "FunctionInUse"
	errFunctionSizeLimitExceeded = "FunctionSizeLimitExceeded"
	errInvalidFunctionAssoc      = "InvalidFunctionAssociation"
)

// CloudFront Functions stages, statuses and limits.
const (
	functionStageDevelopment = "DEVELOPMENT"
	functionStageLive        = "LIVE"

	functionStatusUnpublished  = "UNPUBLISHED"
	functionStatusUnassociated = "UNASSOCIATED"
	functionStatusDeployed     = "DEPLOYED"

	functionMaxCodeSize   = 10 * 1024
	functionMaxNameLength = 64
	functionListMaxItems  = 100
)

// functionRuntimes are the documented CloudFront Functions runtimes.
var functionRuntimes = []string{"cloudfront-js-1.0", "cloudfront-js-2.0"}

// Function is a CloudFront Function. The DEVELOPMENT stage is the latest
// configuration and code; Live is the snapshot taken by the last
// PublishFunction, nil until the function is published.
type Function struct {
	Name        string
	ARN         string
	CreatedTime time.Time
	Development FunctionStage
	Live        *FunctionStage
	// associated is set on the copies storage returns: whether a cache
	// behavior of any distribution is associated with the function.
	associated bool
}

// status is the Status the FunctionSummary of the given stage reports.
func (f *Function) status(stage string) string {
	switch {
	case stage != functionStageLive && !f.Development.Published:
		return functionStatusUnpublished
	case f.associated:
		return functionStatusDeployed
	default:
		return functionStatusUnassociated
	}
}

// FunctionStage is the configuration and code of one function stage.
type FunctionStage struct {
	Comment          string
	Runtime          string
	Code             []byte
	ETag             string
	LastModifiedTime time.Time
	// Published reports whether the DEVELOPMENT code is the one LIVE runs.
	Published bool
}

// stage returns the requested stage, or nil when it does not exist.
func (f *Function) stage(name string) *FunctionStage {
	if name == functionStageLive {
		return f.Live
	}

	return &f.Development
}

func (f *Function) clone() *Function {
	out := *f
	out.Development.Code = append([]byte(nil), f.Development.Code...)

	if f.Live != nil {
		live := *f.Live
		live.Code = append([]byte(nil), f.Live.Code...)
		out.Live = &live
	}

	return &out
}

// FunctionConfigXML is the FunctionConfig element of requests and summaries.
type FunctionConfigXML struct {
	Comment string `xml:"Comment"`
	Runtime string `xml:"Runtime"`
}

// CreateFunctionRequest is the body of CreateFunction. FunctionCode is the
// base64-encoded function code.
type CreateFunctionRequest struct {
	XMLName        xml.Name           `xml:"CreateFunctionRequest"`
	Name           string             `xml:"Name"`
	FunctionConfig *FunctionConfigXML `xml:"FunctionConfig"`
	FunctionCode   string             `xml:"FunctionCode"`
}

// UpdateFunctionRequest is the body of UpdateFunction.
type UpdateFunctionRequest struct {
	XMLName        xml.Name           `xml:"UpdateFunctionRequest"`
	FunctionConfig *FunctionConfigXML `xml:"FunctionConfig"`
	FunctionCode   string             `xml:"FunctionCode"`
}

// FunctionSummaryXML is the FunctionSummary element returned by
// Create/Describe/Update/PublishFunction and listed by ListFunctions.
type FunctionSummaryXML struct {
	XMLName          xml.Name             `xml:"FunctionSummary"`
	Xmlns            string               `xml:"xmlns,attr,omitempty"`
	Name             string               `xml:"Name"`
	Status           string               `xml:"Status"`
	FunctionConfig   *FunctionConfigXML   `xml:"FunctionConfig"`
	FunctionMetadata *FunctionMetadataXML `xml:"FunctionMetadata"`
}

// FunctionMetadataXML is the FunctionMetadata element of a FunctionSummary.
type FunctionMetadataXML struct {
	FunctionARN      string `xml:"FunctionARN"`
	Stage            string `xml:"Stage"`
	CreatedTime      string `xml:"CreatedTime"`
	LastModifiedTime string `xml:"LastModifiedTime"`
}

// FunctionListXML is the body of ListFunctions.
type FunctionListXML struct {
	XMLName    xml.Name             `xml:"FunctionList"`
	Xmlns      string               `xml:"xmlns,attr"`
	NextMarker string               `xml:"NextMarker,omitempty"`
	MaxItems   int                  `xml:"MaxItems"`
	Quantity   int                  `xml:"Quantity"`
	Items      *FunctionSummaryList `xml:"Items,omitempty"`
}

// FunctionSummaryList is the Items element of FunctionList.
type FunctionSummaryList struct {
	FunctionSummary []FunctionSummaryXML `xml:"FunctionSummary"`
}
