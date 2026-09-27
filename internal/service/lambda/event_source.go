package lambda

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Event source mapping polling. When a mapping points at an SQS queue, the
// service polls the queue and invokes the function with aws:sqs events —
// the behavior CreateEventSourceMapping documents but kumo did not perform.

const (
	// esmPollWaitSeconds is the long-poll wait of each SQS ReceiveMessage.
	esmPollWaitSeconds = 1

	// esmPollRetryBackoff backs off after a failed poll or invocation.
	esmPollRetryBackoff = 500 * time.Millisecond
)

// SQSPollerMessage is one SQS message handed to the poller. It carries the
// fields the aws:sqs Lambda event needs.
type SQSPollerMessage struct {
	MessageID         string
	ReceiptHandle     string
	Body              string
	MD5OfBody         string
	Attributes        map[string]string
	MessageAttributes map[string]SQSPollerMessageAttribute
}

// SQSPollerMessageAttribute is an SQS message attribute for the event.
type SQSPollerMessageAttribute struct {
	DataType    string
	StringValue string
	BinaryValue []byte
}

// SQSEventSource abstracts the queue access an event source mapping needs.
// The server wiring adapts the sqs storage to it, keeping the lambda
// package free of an sqs dependency.
type SQSEventSource interface {
	ReceiveMessages(ctx context.Context, queueARN string, maxMessages, waitTimeSeconds int) ([]*SQSPollerMessage, error)
	DeleteMessage(ctx context.Context, queueARN, receiptHandle string) error
}

// sqsEventSourceARN reports whether arn names an SQS queue, the only event
// source the poller currently drives.
func sqsEventSourceARN(arn string) bool {
	return strings.HasPrefix(arn, "arn:aws:sqs:")
}

// SetSQSEventSource installs the SQS event source and starts pollers for
// every enabled SQS mapping that already exists (e.g. restored from a
// persisted snapshot).
func (s *Service) SetSQSEventSource(src SQSEventSource) {
	s.esmMu.Lock()
	s.sqsSource = src
	s.esmMu.Unlock()

	mappings, _, err := s.storage.ListEventSourceMappings(context.Background(), "", "", "", 0)
	if err != nil {
		return
	}

	for _, m := range mappings {
		s.syncEventSourcePoller(m)
	}
}

// syncEventSourcePoller starts or stops a mapping's poller to match its
// enabled state. Called on create/update/delete and after SetSQSEventSource.
func (s *Service) syncEventSourcePoller(mapping *EventSourceMapping) {
	s.esmMu.Lock()
	defer s.esmMu.Unlock()

	if s.esmCancels == nil {
		s.esmCancels = make(map[string]context.CancelFunc)
	}

	enabled := mapping.Enabled == nil || *mapping.Enabled
	running := s.esmCancels[mapping.UUID] != nil

	if !enabled || !sqsEventSourceARN(mapping.EventSourceArn) || s.sqsSource == nil {
		if running {
			s.esmCancels[mapping.UUID]()
			delete(s.esmCancels, mapping.UUID)
		}

		return
	}

	if running {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.esmCancels[mapping.UUID] = cancel

	go s.pollSQSEventSource(ctx, mapping.UUID)
}

// stopEventSourcePoller halts the poller for a deleted mapping.
func (s *Service) stopEventSourcePoller(uuid string) {
	s.esmMu.Lock()
	defer s.esmMu.Unlock()

	if cancel, ok := s.esmCancels[uuid]; ok {
		cancel()
		delete(s.esmCancels, uuid)
	}
}

// stopAllEventSourcePollers halts every running poller (on Close).
func (s *Service) stopAllEventSourcePollers() {
	s.esmMu.Lock()
	defer s.esmMu.Unlock()

	for uuid, cancel := range s.esmCancels {
		cancel()
		delete(s.esmCancels, uuid)
	}
}

// pollSQSEventSource polls the mapping's queue and invokes the function
// until the mapping is deleted, disabled, or the service closes.
func (s *Service) pollSQSEventSource(ctx context.Context, mappingUUID string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		mapping, err := s.storage.GetEventSourceMapping(ctx, mappingUUID)
		if err != nil || (mapping.Enabled != nil && !*mapping.Enabled) {
			return
		}

		src := s.sqsSource
		if src == nil {
			return
		}

		messages, err := src.ReceiveMessages(ctx, mapping.EventSourceArn, mapping.BatchSize, esmPollWaitSeconds)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			slog.Warn("lambda: event source mapping poll failed",
				"mapping", mappingUUID, "source", mapping.EventSourceArn, "error", err)
			sleepOrDone(ctx, esmPollRetryBackoff)

			continue
		}

		if len(messages) == 0 {
			continue
		}

		s.deliverSQSMessages(ctx, mapping, messages)
	}
}

// deliverSQSMessages invokes the function with one aws:sqs event containing
// the whole batch and deletes the messages when the invocation succeeds —
// the all-or-nothing batch semantics AWS documents for SQS mappings.
func (s *Service) deliverSQSMessages(ctx context.Context, mapping *EventSourceMapping, messages []*SQSPollerMessage) {
	payload, err := buildSQSEvent(messages, mapping.EventSourceArn)
	if err != nil {
		slog.Error("lambda: failed to build sqs event", "mapping", mapping.UUID, "error", err)

		return
	}

	if err := s.invokeForMapping(ctx, mapping, payload); err != nil {
		// No delete: the messages become visible again after their
		// visibility timeout and are retried, matching AWS.
		slog.Warn("lambda: event source invocation failed, batch returns to queue",
			"mapping", mapping.UUID, "error", err)
		sleepOrDone(ctx, esmPollRetryBackoff)

		return
	}

	src := s.sqsSource

	for _, msg := range messages {
		if err := src.DeleteMessage(ctx, mapping.EventSourceArn, msg.ReceiptHandle); err != nil {
			slog.Warn("lambda: failed to delete processed message",
				"mapping", mapping.UUID, "messageId", msg.MessageID, "error", err)
		}
	}
}

// invokeForMapping invokes the mapped function synchronously using the same
// resolution order as Invoke: a Runtime API handler wins, then the
// function's InvokeEndpoint.
func (s *Service) invokeForMapping(ctx context.Context, mapping *EventSourceMapping, payload []byte) error {
	fn, err := s.storage.GetFunctionByARN(ctx, mapping.FunctionArn)
	if err != nil {
		return fmt.Errorf("function %s: %w", mapping.FunctionArn, err)
	}

	if s.broker.registered(fn.FunctionName) {
		res := (&runtimeDeliverer{broker: s.broker, fn: fn.FunctionName}).deliver(ctx, fn.FunctionName, payload)
		if res != asyncDelivered {
			return fmt.Errorf("runtime delivery failed (result %d)", res)
		}

		return nil
	}

	if fn.InvokeEndpoint != "" {
		res := (&endpointDeliverer{
			client:   s.async.client,
			endpoint: fn.InvokeEndpoint,
			gate:     s.async.gate(fn.FunctionName),
		}).deliver(ctx, fn.FunctionName, payload)

		if res != asyncDelivered {
			return fmt.Errorf("endpoint delivery failed (result %d)", res)
		}

		return nil
	}

	return errors.New("function has no runtime handler or InvokeEndpoint")
}

// sleepOrDone sleeps for d or returns early when ctx is canceled.
func sleepOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// sqsEventRecord is one Records[] element of the aws:sqs Lambda event.
type sqsEventRecord struct {
	MessageID         string                         `json:"messageId"`
	ReceiptHandle     string                         `json:"receiptHandle"`
	Body              string                         `json:"body"`
	Attributes        map[string]string              `json:"attributes"`
	MessageAttributes map[string]sqsEventMessageAttr `json:"messageAttributes"`
	MD5OfBody         string                         `json:"md5OfBody"`
	EventSource       string                         `json:"eventSource"`
	EventSourceARN    string                         `json:"eventSourceARN"`
	AWSRegion         string                         `json:"awsRegion"`
	MD5OfMessageAttrs string                         `json:"md5OfMessageAttributes,omitempty"`
}

// sqsEventMessageAttr is the Lambda event form of an SQS message attribute.
type sqsEventMessageAttr struct {
	StringValue      *string  `json:"stringValue,omitempty"`
	BinaryValue      *string  `json:"binaryValue,omitempty"`
	StringListValues []string `json:"stringListValues"`
	BinaryListValues []string `json:"binaryListValues"`
	DataType         string   `json:"dataType"`
}

// buildSQSEvent renders the aws:sqs event document AWS sends to the
// function for one batch of SQS messages.
func buildSQSEvent(messages []*SQSPollerMessage, queueARN string) ([]byte, error) {
	// arn:aws:sqs:<region>:<account>:<name>
	region := "us-east-1"
	if parts := strings.Split(queueARN, ":"); len(parts) > 3 {
		region = parts[3]
	}

	records := make([]sqsEventRecord, len(messages))

	for i, msg := range messages {
		attrs := make(map[string]sqsEventMessageAttr, len(msg.MessageAttributes))

		for name, a := range msg.MessageAttributes {
			entry := sqsEventMessageAttr{
				DataType:         a.DataType,
				StringListValues: []string{},
				BinaryListValues: []string{},
			}

			if a.StringValue != "" {
				entry.StringValue = &a.StringValue
			}

			if len(a.BinaryValue) > 0 {
				encoded := base64.StdEncoding.EncodeToString(a.BinaryValue)
				entry.BinaryValue = &encoded
			}

			attrs[name] = entry
		}

		records[i] = sqsEventRecord{
			MessageID:         msg.MessageID,
			ReceiptHandle:     msg.ReceiptHandle,
			Body:              msg.Body,
			Attributes:        msg.Attributes,
			MessageAttributes: attrs,
			MD5OfBody:         msg.MD5OfBody,
			EventSource:       "aws:sqs",
			EventSourceARN:    queueARN,
			AWSRegion:         region,
		}
	}

	return json.Marshal(map[string]any{"Records": records})
}
