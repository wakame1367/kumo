package sns

import (
	"context"
	"testing"
)

// newFIFOTopic creates a FIFO topic with the given attributes.
func newFIFOTopic(t *testing.T, storage *MemoryStorage, attributes map[string]string) string {
	t.Helper()

	if attributes == nil {
		attributes = map[string]string{}
	}

	attributes["FifoTopic"] = "true"

	topic, err := storage.CreateTopic(context.Background(), "test.fifo", attributes, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	return topic.ARN
}

func TestCreateTopic_FifoNameRequiresFifoAttribute(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	ctx := context.Background()

	// .fifo name without the attribute.
	if _, err := storage.CreateTopic(ctx, "name.fifo", nil, nil); err == nil {
		t.Fatal("CreateTopic(.fifo name without FifoTopic) want error")
	}

	// FifoTopic=true without a .fifo name.
	if _, err := storage.CreateTopic(ctx, "name", map[string]string{"FifoTopic": "true"}, nil); err == nil {
		t.Fatal("CreateTopic(FifoTopic without .fifo name) want error")
	}

	// A matching pair is accepted and marks the topic FIFO.
	topic, err := storage.CreateTopic(ctx, "name.fifo", map[string]string{"FifoTopic": "true"}, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	if !topic.Fifo {
		t.Error("topic.Fifo = false, want true")
	}
}

func TestCreateTopic_ExistingTopicWithDifferentAttributes(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	ctx := context.Background()

	if _, err := storage.CreateTopic(ctx, "dup", map[string]string{"DisplayName": "a"}, nil); err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	// Same attributes: idempotent.
	if _, err := storage.CreateTopic(ctx, "dup", map[string]string{"DisplayName": "a"}, nil); err != nil {
		t.Fatalf("CreateTopic(same attrs) error = %v", err)
	}

	// Different attributes: rejected.
	if _, err := storage.CreateTopic(ctx, "dup", map[string]string{"DisplayName": "b"}, nil); err == nil {
		t.Fatal("CreateTopic(different attrs) want error")
	}

	// Different tags: rejected.
	if _, err := storage.CreateTopic(ctx, "dup", map[string]string{"DisplayName": "a"}, []Tag{{Key: "k", Value: "v"}}); err == nil {
		t.Fatal("CreateTopic(different tags) want error")
	}
}

func TestPublish_FifoTopicRequiresMessageGroupID(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	topicARN := newFIFOTopic(t, storage, nil)

	result, err := storage.Publish(context.Background(), topicARN, "hello", "", "", "", "dedup-1", nil)
	expectTopicErrorCode(t, err, errInvalidParameter)

	if result != nil {
		t.Errorf("Publish() result = %+v, want nil", result)
	}
}

func TestPublish_FifoTopicRequiresDeduplicationID(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	topicARN := newFIFOTopic(t, storage, nil)

	// No ContentBasedDeduplication and no explicit ID: rejected.
	result, err := storage.Publish(context.Background(), topicARN, "hello", "", "", "group-1", "", nil)
	expectTopicErrorCode(t, err, errInvalidParameter)

	if result != nil {
		t.Errorf("Publish() result = %+v, want nil", result)
	}
}

func TestPublish_FifoContentBasedDeduplication(t *testing.T) {
	t.Parallel()

	publisher := &capturingPublisher{}
	storage := NewMemoryStorage("http://localhost:4566")
	storage.SetSQSPublisher(publisher)

	ctx := context.Background()
	topicARN := newFIFOTopic(t, storage, map[string]string{"ContentBasedDeduplication": "true"})

	if _, err := storage.Subscribe(ctx, topicARN, protocolSQS, "arn:aws:sqs:us-east-1:000000000000:q.fifo", nil); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	result, err := storage.Publish(ctx, topicARN, "hello", "", "", "group-1", "", nil)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if result.SequenceNumber == "" {
		t.Error("Publish() SequenceNumber = \"\", want non-empty for FIFO topics")
	}

	// The derived deduplication ID is forwarded so the FIFO queue dedupes.
	if publisher.dedupID == "" || publisher.groupID != "group-1" {
		t.Errorf("delivery groupID=%q dedupID=%q, want group-1 and derived ID", publisher.groupID, publisher.dedupID)
	}
}

func TestPublish_FifoSequenceNumberIncrements(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	ctx := context.Background()
	topicARN := newFIFOTopic(t, storage, nil)

	first, err := storage.Publish(ctx, topicARN, "m1", "", "", "g", "d1", nil)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	second, err := storage.Publish(ctx, topicARN, "m2", "", "", "g", "d2", nil)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if first.SequenceNumber != "1" || second.SequenceNumber != "2" {
		t.Errorf("SequenceNumbers = %q, %q; want 1, 2", first.SequenceNumber, second.SequenceNumber)
	}
}

func TestPublish_FifoDeduplicatesWithinWindow(t *testing.T) {
	t.Parallel()

	publisher := &capturingPublisher{}
	storage := NewMemoryStorage("http://localhost:4566")
	storage.SetSQSPublisher(publisher)

	ctx := context.Background()
	topicARN := newFIFOTopic(t, storage, nil)

	if _, err := storage.Subscribe(ctx, topicARN, protocolSQS, "arn:aws:sqs:us-east-1:000000000000:q.fifo", nil); err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	first, err := storage.Publish(ctx, topicARN, "hello", "", "", "g", "dup-id", nil)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	second, err := storage.Publish(ctx, topicARN, "hello", "", "", "g", "dup-id", nil)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if second.MessageID != first.MessageID || second.SequenceNumber != first.SequenceNumber {
		t.Errorf("duplicate Publish() = (%q, %q), want original (%q, %q)",
			second.MessageID, second.SequenceNumber, first.MessageID, first.SequenceNumber)
	}

	if publisher.calls != 1 {
		t.Errorf("publisher.calls = %d, want 1 (duplicate must not be delivered)", publisher.calls)
	}
}

func TestPublish_StandardTopicRejectsDeduplicationID(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	ctx := context.Background()

	topic, err := storage.CreateTopic(ctx, "std", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	result, err := storage.Publish(ctx, topic.ARN, "hello", "", "", "", "dedup-1", nil)
	expectTopicErrorCode(t, err, errInvalidParameter)

	if result != nil {
		t.Errorf("Publish() result = %+v, want nil", result)
	}
}

func TestSubscribe_FifoPairingRules(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	ctx := context.Background()

	fifoARN := newFIFOTopic(t, storage, nil)

	std, err := storage.CreateTopic(ctx, "std", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	// A FIFO queue cannot hang off a standard topic.
	if _, err := storage.Subscribe(ctx, std.ARN, protocolSQS, "arn:aws:sqs:us-east-1:000000000000:q.fifo", nil); err == nil {
		t.Error("Subscribe(standard topic, fifo queue) want error")
	}

	// A FIFO topic accepts only sqs subscriptions.
	if _, err := storage.Subscribe(ctx, fifoARN, protocolLambda, "arn:aws:lambda:us-east-1:000000000000:function:fn", nil); err == nil {
		t.Error("Subscribe(fifo topic, lambda) want error")
	}

	if _, err := storage.Subscribe(ctx, fifoARN, protocolSQS, "arn:aws:sqs:us-east-1:000000000000:q.fifo", nil); err != nil {
		t.Errorf("Subscribe(fifo topic, fifo queue) error = %v", err)
	}
}
