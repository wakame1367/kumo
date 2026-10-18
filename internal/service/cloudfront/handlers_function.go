package cloudfront

import (
	"net/http"
	"strconv"
	"time"
)

const functionPath = "/2020-05-31/function/"

// CreateFunction handles POST /2020-05-31/function.
func (s *Service) CreateFunction(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeXMLBody[CreateFunctionRequest](w, r)
	if !ok {
		return
	}

	fn, err := s.storage.CreateFunction(r.Context(), req)
	if err != nil {
		handleFunctionStorageError(w, err)

		return
	}

	w.Header().Set("ETag", fn.Development.ETag)
	w.Header().Set("Location", functionPath+fn.Name)
	writeXMLResponse(w, http.StatusCreated, buildFunctionSummaryXML(fn, functionStageDevelopment, cloudfrontXmlns))
}

// DescribeFunction handles GET /2020-05-31/function/{name}/describe.
func (s *Service) DescribeFunction(w http.ResponseWriter, r *http.Request) {
	fn, stage, ok := s.functionStage(w, r)
	if !ok {
		return
	}

	w.Header().Set("ETag", fn.stage(stage).ETag)
	writeXMLResponse(w, http.StatusOK, buildFunctionSummaryXML(fn, stage, cloudfrontXmlns))
}

// GetFunction handles GET /2020-05-31/function/{name}: the response body is
// the raw function code of the requested stage.
func (s *Service) GetFunction(w http.ResponseWriter, r *http.Request) {
	fn, stage, ok := s.functionStage(w, r)
	if !ok {
		return
	}

	st := fn.stage(stage)

	w.Header().Set("ETag", st.ETag)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(st.Code)
}

// UpdateFunction handles PUT /2020-05-31/function/{name}.
func (s *Service) UpdateFunction(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeXMLBody[UpdateFunctionRequest](w, r)
	if !ok {
		return
	}

	fn, err := s.storage.UpdateFunction(r.Context(), r.PathValue("name"), req, r.Header.Get("If-Match"))
	if err != nil {
		handleFunctionStorageError(w, err)

		return
	}

	w.Header().Set("ETag", fn.Development.ETag)
	writeXMLResponse(w, http.StatusOK, buildFunctionSummaryXML(fn, functionStageDevelopment, cloudfrontXmlns))
}

// PublishFunction handles POST /2020-05-31/function/{name}/publish.
func (s *Service) PublishFunction(w http.ResponseWriter, r *http.Request) {
	fn, err := s.storage.PublishFunction(r.Context(), r.PathValue("name"), r.Header.Get("If-Match"))
	if err != nil {
		handleFunctionStorageError(w, err)

		return
	}

	w.Header().Set("ETag", fn.Live.ETag)
	writeXMLResponse(w, http.StatusOK, buildFunctionSummaryXML(fn, functionStageLive, cloudfrontXmlns))
}

// DeleteFunction handles DELETE /2020-05-31/function/{name}.
func (s *Service) DeleteFunction(w http.ResponseWriter, r *http.Request) {
	if err := s.storage.DeleteFunction(r.Context(), r.PathValue("name"), r.Header.Get("If-Match")); err != nil {
		handleFunctionStorageError(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListFunctions handles GET /2020-05-31/function.
func (s *Service) ListFunctions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	maxItems := functionListMaxItems

	if raw := query.Get("MaxItems"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeCloudFrontError(w, errInvalidArgument, "MaxItems must be a positive integer.", http.StatusBadRequest)

			return
		}

		maxItems = n
	}

	stage, ok := parseFunctionStage(w, query.Get("Stage"))
	if !ok {
		return
	}

	items, nextMarker, err := s.storage.ListFunctions(r.Context(), stage, query.Get("Marker"), maxItems)
	if err != nil {
		handleFunctionStorageError(w, err)

		return
	}

	list := &FunctionListXML{Xmlns: cloudfrontXmlns, NextMarker: nextMarker, MaxItems: maxItems, Quantity: len(items)}

	if len(items) > 0 {
		list.Items = &FunctionSummaryList{}

		for _, fn := range items {
			list.Items.FunctionSummary = append(list.Items.FunctionSummary, *buildFunctionSummaryXML(fn, stage, ""))
		}
	}

	writeXMLResponse(w, http.StatusOK, list)
}

// functionStage loads the function named in the path and resolves the
// ?Stage= query (DEVELOPMENT by default). A stage the function does not have
// (LIVE before the first publish) is NoSuchFunctionExists. On failure the
// error response has been written.
func (s *Service) functionStage(w http.ResponseWriter, r *http.Request) (*Function, string, bool) {
	stage, ok := parseFunctionStage(w, r.URL.Query().Get("Stage"))
	if !ok {
		return nil, "", false
	}

	fn, err := s.storage.GetFunction(r.Context(), r.PathValue("name"))
	if err != nil {
		handleFunctionStorageError(w, err)

		return nil, "", false
	}

	if fn.stage(stage) == nil {
		handleFunctionStorageError(w, noSuchFunction())

		return nil, "", false
	}

	return fn, stage, true
}

// parseFunctionStage validates a Stage parameter, defaulting to DEVELOPMENT.
func parseFunctionStage(w http.ResponseWriter, raw string) (string, bool) {
	switch raw {
	case "", functionStageDevelopment:
		return functionStageDevelopment, true
	case functionStageLive:
		return functionStageLive, true
	default:
		writeCloudFrontError(w, errInvalidArgument, "Stage must be one of DEVELOPMENT, LIVE.", http.StatusBadRequest)

		return "", false
	}
}

func buildFunctionSummaryXML(fn *Function, stage, xmlns string) *FunctionSummaryXML {
	st := fn.stage(stage)

	return &FunctionSummaryXML{
		Xmlns:          xmlns,
		Name:           fn.Name,
		Status:         fn.status(stage),
		FunctionConfig: &FunctionConfigXML{Comment: st.Comment, Runtime: st.Runtime},
		FunctionMetadata: &FunctionMetadataXML{
			FunctionARN:      fn.ARN,
			Stage:            stage,
			CreatedTime:      fn.CreatedTime.Format(time.RFC3339),
			LastModifiedTime: st.LastModifiedTime.Format(time.RFC3339),
		},
	}
}

// handleFunctionStorageError maps a function storage error to the status the
// API reference documents.
func handleFunctionStorageError(w http.ResponseWriter, err error) {
	writePolicyError(w, err, errNoSuchFunctionExists, errFunctionAlreadyExists, errFunctionInUse)
}
