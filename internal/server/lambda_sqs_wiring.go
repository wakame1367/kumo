package server

import (
	"context"
	"strings"

	"github.com/sivchari/kumo/internal/service"
	"github.com/sivchari/kumo/internal/service/lambda"
	"github.com/sivchari/kumo/internal/service/sqs"
)

// wireLambdaToSQS connects the Lambda service to the SQS service so event
// source mappings pointing at SQS queue ARNs actually poll the queue and
// invoke the function.
//
// Without this wiring, CreateEventSourceMapping only persisted the mapping
// and no messages ever reached the function.
func wireLambdaToSQS(registry *service.Registry) {
	lambdaSvc, ok := registry.Get("lambda")
	if !ok {
		return
	}

	sqsSvc, ok := registry.Get("sqs")
	if !ok {
		return
	}

	lambdaTyped, ok := lambdaSvc.(*lambda.Service)
	if !ok {
		return
	}

	sqsTyped, ok := sqsSvc.(*sqs.Service)
	if !ok {
		return
	}

	lambdaTyped.SetSQSEventSource(&sqsEventSourceAdapter{
		storage: sqsTyped.Storage(),
		baseURL: sqsTyped.BaseURL(),
	})
}

// sqsEventSourceAdapter adapts the SQS storage layer to the lambda package's
// SQSEventSource interface, translating queue ARNs into the queue URLs the
// storage layer keys queues by.
type sqsEventSourceAdapter struct {
	storage sqs.Storage
	baseURL string
}

// arnToQueueURL converts an SQS ARN to a queue URL. A non-ARN input is
// returned unchanged so direct URLs keep working.
func (a *sqsEventSourceAdapter) arnToQueueURL(arn string) string {
	if !strings.HasPrefix(arn, "arn:") {
		return arn
	}

	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return arn
	}

	return a.baseURL + "/" + parts[4] + "/" + parts[5]
}

// ReceiveMessages receives up to maxMessages from the queue, long-polling
// for waitTimeSeconds.
func (a *sqsEventSourceAdapter) ReceiveMessages(ctx context.Context, queueARN string, maxMessages, waitTimeSeconds int) ([]*lambda.SQSPollerMessage, error) {
	messages, err := a.storage.ReceiveMessage(ctx, a.arnToQueueURL(queueARN), maxMessages, 0, waitTimeSeconds)
	if err != nil {
		return nil, err //nolint:wrapcheck // adapter is a thin pass-through
	}

	out := make([]*lambda.SQSPollerMessage, len(messages))

	for i, msg := range messages {
		attrs := make(map[string]lambda.SQSPollerMessageAttribute, len(msg.MessageAttributes))
		for name, a := range msg.MessageAttributes {
			attrs[name] = lambda.SQSPollerMessageAttribute{
				DataType:    a.DataType,
				StringValue: a.StringValue,
				BinaryValue: a.BinaryValue,
			}
		}

		out[i] = &lambda.SQSPollerMessage{
			MessageID:         msg.MessageID,
			ReceiptHandle:     msg.ReceiptHandle,
			Body:              msg.Body,
			MD5OfBody:         msg.MD5OfBody,
			Attributes:        msg.Attributes,
			MessageAttributes: attrs,
		}
	}

	return out, nil
}

// DeleteMessage deletes a processed message from the queue.
func (a *sqsEventSourceAdapter) DeleteMessage(ctx context.Context, queueARN, receiptHandle string) error {
	return a.storage.DeleteMessage(ctx, a.arnToQueueURL(queueARN), receiptHandle) //nolint:wrapcheck // thin pass-through
}
