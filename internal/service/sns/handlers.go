package sns

import (
	"encoding/xml"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/sivchari/kumo/internal/service"
)

const snsXMLNS = "http://sns.amazonaws.com/doc/2010-03-31/"

// Error codes for SNS.
const (
	errNotFound             = "NotFound"
	errInvalidParameter     = "InvalidParameter"
	errInternalServiceError = "InternalError"
	errInvalidAction        = "InvalidAction"

	// PublishBatch request-level error codes.
	errEmptyBatch       = "EmptyBatchRequest"
	errTooManyEntries   = "TooManyEntriesInBatchRequest"
	errIDsNotDistinct   = "BatchEntryIdsNotDistinct"
	errInvalidBatchID   = "InvalidBatchEntryId"
	maxBatchEntries     = 10
	batchEntryIDPattern = `^[0-9A-Za-z_-]{1,80}$`
)

// batchEntryIDRe matches the characters SNS allows in a batch entry Id.
var batchEntryIDRe = regexp.MustCompile(batchEntryIDPattern)

// CreateTopic handles the CreateTopic action.
func (s *Service) CreateTopic(w http.ResponseWriter, r *http.Request) {
	var req CreateTopicRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	if req.Name == "" {
		writeTopicError(w, errInvalidParameter, "Topic name is required", http.StatusBadRequest)

		return
	}

	topic, err := s.storage.CreateTopic(r.Context(), req.Name, req.Attributes, req.Tags)
	if err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			writeTopicError(w, sErr.Code, sErr.Message, http.StatusBadRequest)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	writeXMLResponse(w, XMLCreateTopicResponse{
		Xmlns: snsXMLNS,
		CreateTopicResult: XMLCreateTopicResult{
			TopicArn: topic.ARN,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// DeleteTopic handles the DeleteTopic action.
func (s *Service) DeleteTopic(w http.ResponseWriter, r *http.Request) {
	var req DeleteTopicRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	if req.TopicARN == "" {
		writeTopicError(w, errInvalidParameter, "TopicArn is required", http.StatusBadRequest)

		return
	}

	err := s.storage.DeleteTopic(r.Context(), req.TopicARN)
	if err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			status := http.StatusBadRequest
			if sErr.Code == errNotFound {
				status = http.StatusNotFound
			}

			writeTopicError(w, sErr.Code, sErr.Message, status)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	// DeleteTopic returns empty response on success.
	writeXMLResponse(w, XMLDeleteTopicResponse{
		Xmlns: snsXMLNS,
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// ListTopics handles the ListTopics action.
func (s *Service) ListTopics(w http.ResponseWriter, r *http.Request) {
	var req ListTopicsRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	topics, nextToken, err := s.storage.ListTopics(r.Context(), req.NextToken)
	if err != nil {
		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	topicMembers := make([]XMLTopicMember, 0, len(topics))
	for _, topic := range topics {
		topicMembers = append(topicMembers, XMLTopicMember{
			TopicArn: topic.ARN,
		})
	}

	writeXMLResponse(w, XMLListTopicsResponse{
		Xmlns: snsXMLNS,
		ListTopicsResult: XMLListTopicsResult{
			Topics: XMLTopics{
				Member: topicMembers,
			},
			NextToken: nextToken,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// Subscribe handles the Subscribe action.
func (s *Service) Subscribe(w http.ResponseWriter, r *http.Request) {
	var req SubscribeRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	if req.TopicARN == "" {
		writeTopicError(w, errInvalidParameter, "TopicArn is required", http.StatusBadRequest)

		return
	}

	if req.Protocol == "" {
		writeTopicError(w, errInvalidParameter, "Protocol is required", http.StatusBadRequest)

		return
	}

	subscription, err := s.storage.Subscribe(r.Context(), req.TopicARN, req.Protocol, req.Endpoint, req.Attributes)
	if err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			status := http.StatusBadRequest
			if sErr.Code == errNotFound {
				status = http.StatusNotFound
			}

			writeTopicError(w, sErr.Code, sErr.Message, status)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	// SNS answers "pending confirmation" instead of the subscription ARN
	// for protocols that still need ConfirmSubscription, unless the caller
	// opted into the real ARN with ReturnSubscriptionArn.
	subscriptionARN := subscription.ARN
	if !subscription.Confirmed && !req.ReturnSubscriptionArn {
		subscriptionARN = "pending confirmation"
	}

	writeXMLResponse(w, XMLSubscribeResponse{
		Xmlns: snsXMLNS,
		SubscribeResult: XMLSubscribeResult{
			SubscriptionArn: subscriptionARN,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// ConfirmSubscription handles the ConfirmSubscription action.
func (s *Service) ConfirmSubscription(w http.ResponseWriter, r *http.Request) {
	var req ConfirmSubscriptionRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	if req.TopicArn == "" {
		writeTopicError(w, errInvalidParameter, "TopicArn is required", http.StatusBadRequest)

		return
	}

	if req.Token == "" {
		writeTopicError(w, errInvalidParameter, "Token is required", http.StatusBadRequest)

		return
	}

	subscription, err := s.storage.ConfirmSubscription(r.Context(), req.TopicArn, req.Token)
	if err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			status := http.StatusBadRequest
			if sErr.Code == errNotFound {
				status = http.StatusNotFound
			}

			writeTopicError(w, sErr.Code, sErr.Message, status)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	writeXMLResponse(w, XMLConfirmSubscriptionResponse{
		Xmlns: snsXMLNS,
		ConfirmSubscriptionResult: XMLConfirmSubscriptionResult{
			SubscriptionArn: subscription.ARN,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// Unsubscribe handles the Unsubscribe action.
func (s *Service) Unsubscribe(w http.ResponseWriter, r *http.Request) {
	var req UnsubscribeRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	if req.SubscriptionARN == "" {
		writeTopicError(w, errInvalidParameter, "SubscriptionArn is required", http.StatusBadRequest)

		return
	}

	err := s.storage.Unsubscribe(r.Context(), req.SubscriptionARN)
	if err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			status := http.StatusBadRequest
			if sErr.Code == errNotFound {
				status = http.StatusNotFound
			}

			writeTopicError(w, sErr.Code, sErr.Message, status)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	// Unsubscribe returns empty response on success.
	writeXMLResponse(w, XMLUnsubscribeResponse{
		Xmlns: snsXMLNS,
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// Publish handles the Publish action.
func (s *Service) Publish(w http.ResponseWriter, r *http.Request) {
	var req PublishRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	topicARN := req.TopicARN
	if topicARN == "" {
		topicARN = req.TargetARN
	}

	if topicARN == "" {
		writeTopicError(w, errInvalidParameter, "TopicArn or TargetArn is required", http.StatusBadRequest)

		return
	}

	if req.Message == "" {
		writeTopicError(w, errInvalidParameter, "Message is required", http.StatusBadRequest)

		return
	}

	result, err := s.storage.Publish(r.Context(), topicARN, req.Message, req.MessageStructure, req.Subject, req.MessageGroupID, req.MessageDeduplicationID, req.MessageAttributes)
	if err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			status := http.StatusBadRequest
			if sErr.Code == errNotFound {
				status = http.StatusNotFound
			}

			writeTopicError(w, sErr.Code, sErr.Message, status)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	writeXMLResponse(w, XMLPublishResponse{
		Xmlns: snsXMLNS,
		PublishResult: XMLPublishResult{
			MessageID:      result.MessageID,
			SequenceNumber: result.SequenceNumber,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// PublishBatch handles the PublishBatch action.
func (s *Service) PublishBatch(w http.ResponseWriter, r *http.Request) {
	var req PublishBatchRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	if req.TopicARN == "" {
		writeTopicError(w, errInvalidParameter, "TopicArn is required", http.StatusBadRequest)

		return
	}

	switch {
	case len(req.PublishBatchRequestEntries) == 0:
		writeTopicError(w, errEmptyBatch, "The batch request doesn't contain any entries.", http.StatusBadRequest)

		return
	case len(req.PublishBatchRequestEntries) > maxBatchEntries:
		writeTopicError(w, errTooManyEntries, "The batch request contains more entries than permissible.", http.StatusBadRequest)

		return
	}

	seen := make(map[string]bool, len(req.PublishBatchRequestEntries))

	for _, entry := range req.PublishBatchRequestEntries {
		if !batchEntryIDRe.MatchString(entry.ID) {
			writeTopicError(w, errInvalidBatchID, "A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long.", http.StatusBadRequest)

			return
		}

		if seen[entry.ID] {
			writeTopicError(w, errIDsNotDistinct, "Two or more batch entries in the request have the same Id.", http.StatusBadRequest)

			return
		}

		seen[entry.ID] = true
	}

	// A missing topic fails the whole request rather than each entry.
	if _, err := s.storage.GetTopic(r.Context(), req.TopicARN); err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			writeTopicError(w, sErr.Code, sErr.Message, http.StatusNotFound)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	successful := XMLSuccessfulEntries{Member: []XMLPublishBatchResultEntry{}}
	failed := XMLFailedEntries{Member: []XMLBatchResultErrorEntry{}}

	for _, entry := range req.PublishBatchRequestEntries {
		result, err := s.storage.Publish(r.Context(), req.TopicARN, entry.Message, entry.MessageStructure,
			entry.Subject, entry.MessageGroupID, entry.MessageDeduplicationID, entry.MessageAttributes)
		if err != nil {
			code, message, senderFault := errInternalServiceError, "Internal server error", false

			var sErr *TopicError
			if errors.As(err, &sErr) {
				code, message, senderFault = sErr.Code, sErr.Message, true
			}

			failed.Member = append(failed.Member, XMLBatchResultErrorEntry{
				ID: entry.ID, Code: code, Message: message, SenderFault: senderFault,
			})

			continue
		}

		successful.Member = append(successful.Member, XMLPublishBatchResultEntry{
			ID: entry.ID, MessageID: result.MessageID, SequenceNumber: result.SequenceNumber,
		})
	}

	writeXMLResponse(w, XMLPublishBatchResponse{
		Xmlns: snsXMLNS,
		PublishBatchResult: XMLPublishBatchResult{
			Successful: successful,
			Failed:     failed,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// ListSubscriptions handles the ListSubscriptions action.
func (s *Service) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	var req ListSubscriptionsRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	subscriptions, nextToken, err := s.storage.ListSubscriptions(r.Context(), req.NextToken)
	if err != nil {
		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	members := convertSubscriptionsToXMLMembers(subscriptions)

	writeXMLResponse(w, XMLListSubscriptionsResponse{
		Xmlns: snsXMLNS,
		ListSubscriptionsResult: XMLListSubscriptionsResult{
			Subscriptions: XMLSubscriptions{
				Member: members,
			},
			NextToken: nextToken,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// ListSubscriptionsByTopic handles the ListSubscriptionsByTopic action.
func (s *Service) ListSubscriptionsByTopic(w http.ResponseWriter, r *http.Request) {
	var req ListSubscriptionsByTopicRequest
	if err := service.ReadJSONRequest(r, &req); err != nil {
		writeTopicError(w, errInvalidParameter, "Failed to parse request body", http.StatusBadRequest)

		return
	}

	if req.TopicARN == "" {
		writeTopicError(w, errInvalidParameter, "TopicArn is required", http.StatusBadRequest)

		return
	}

	subscriptions, nextToken, err := s.storage.ListSubscriptionsByTopic(r.Context(), req.TopicARN, req.NextToken)
	if err != nil {
		var sErr *TopicError
		if errors.As(err, &sErr) {
			status := http.StatusBadRequest
			if sErr.Code == errNotFound {
				status = http.StatusNotFound
			}

			writeTopicError(w, sErr.Code, sErr.Message, status)

			return
		}

		writeTopicError(w, errInternalServiceError, "Internal server error", http.StatusInternalServerError)

		return
	}

	members := convertSubscriptionsToXMLMembers(subscriptions)

	writeXMLResponse(w, XMLListSubscriptionsByTopicResponse{
		Xmlns: snsXMLNS,
		ListSubscriptionsByTopicResult: XMLListSubscriptionsByTopicResult{
			Subscriptions: XMLSubscriptions{
				Member: members,
			},
			NextToken: nextToken,
		},
		ResponseMetadata: ResponseMetadata{
			RequestID: uuid.New().String(),
		},
	})
}

// convertSubscriptionsToXMLMembers converts subscriptions to XML members.
func convertSubscriptionsToXMLMembers(subscriptions []*Subscription) []XMLSubscriptionMember {
	members := make([]XMLSubscriptionMember, 0, len(subscriptions))

	for _, sub := range subscriptions {
		members = append(members, XMLSubscriptionMember{
			SubscriptionArn: sub.ARN,
			Owner:           sub.Owner,
			Protocol:        sub.Protocol,
			Endpoint:        sub.Endpoint,
			TopicArn:        sub.TopicARN,
		})
	}

	return members
}

// writeXMLResponse writes an XML response with HTTP 200 OK.
func writeXMLResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("x-amzn-RequestId", uuid.New().String())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(v)
}

// writeTopicError writes an SNS error response in XML format.
func writeTopicError(w http.ResponseWriter, code, message string, status int) {
	requestID := uuid.New().String()

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("x-amzn-RequestId", requestID)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(xml.Header))
	_ = xml.NewEncoder(w).Encode(XMLErrorResponse{
		Xmlns: snsXMLNS,
		Error: XMLErrorDetail{
			Type:    "Sender",
			Code:    code,
			Message: message,
		},
		RequestID: requestID,
	})
}

// actionHandlers returns a map of action names to handler functions.
func (s *Service) actionHandlers() map[string]func(http.ResponseWriter, *http.Request) {
	return map[string]func(http.ResponseWriter, *http.Request){
		"CreateTopic":               s.CreateTopic,
		"DeleteTopic":               s.DeleteTopic,
		"ListTopics":                s.ListTopics,
		"Subscribe":                 s.Subscribe,
		"ConfirmSubscription":       s.ConfirmSubscription,
		"Unsubscribe":               s.Unsubscribe,
		"Publish":                   s.Publish,
		"PublishBatch":              s.PublishBatch,
		"ListSubscriptions":         s.ListSubscriptions,
		"ListSubscriptionsByTopic":  s.ListSubscriptionsByTopic,
		"GetSubscriptionAttributes": s.GetSubscriptionAttributes,
		"SetSubscriptionAttributes": s.SetSubscriptionAttributes,
		"GetTopicAttributes":        s.GetTopicAttributes,
		"SetTopicAttributes":        s.SetTopicAttributes,
		"ListTagsForResource":       s.ListTagsForResource,
		"TagResource":               s.TagResource,
		"UntagResource":             s.UntagResource,
	}
}

// DispatchAction routes the request to the appropriate handler based on X-Amz-Target header.
// This method implements the JSONProtocolService interface.
func (s *Service) DispatchAction(w http.ResponseWriter, r *http.Request) {
	target := r.Header.Get("X-Amz-Target")
	action := strings.TrimPrefix(target, "AmazonSimpleNotificationService.")

	handlers := s.actionHandlers()
	if handler, ok := handlers[action]; ok {
		handler(w, r)

		return
	}

	writeTopicError(w, errInvalidAction, "The action "+action+" is not valid", http.StatusBadRequest)
}
