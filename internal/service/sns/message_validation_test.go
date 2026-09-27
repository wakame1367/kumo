package sns

import (
	"context"
	"strings"
	"testing"
)

// newPlainTopic creates a standard topic and returns its ARN.
func newPlainTopic(t *testing.T, storage *MemoryStorage) string {
	t.Helper()

	topic, err := storage.CreateTopic(context.Background(), "validation-topic", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	return topic.ARN
}

func TestPublish_MessageAttributeValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name  string
		attrs map[string]MessageAttribute
	}{
		{
			name:  "invalid name",
			attrs: map[string]MessageAttribute{"bad name!": {DataType: "String", StringValue: "v"}},
		},
		{
			name:  "empty name",
			attrs: map[string]MessageAttribute{"": {DataType: "String", StringValue: "v"}},
		},
		{
			name:  "unsupported data type",
			attrs: map[string]MessageAttribute{"a": {DataType: "Integer", StringValue: "1"}},
		},
		{
			name:  "empty data type",
			attrs: map[string]MessageAttribute{"a": {StringValue: "v"}},
		},
		{
			name:  "empty string value",
			attrs: map[string]MessageAttribute{"a": {DataType: "String"}},
		},
		{
			name:  "non-numeric number value",
			attrs: map[string]MessageAttribute{"a": {DataType: "Number", StringValue: "abc"}},
		},
		{
			name:  "binary without binary value",
			attrs: map[string]MessageAttribute{"a": {DataType: "Binary", StringValue: "v"}},
		},
		{
			name:  "String.Array without a JSON array",
			attrs: map[string]MessageAttribute{"a": {DataType: "String.Array", StringValue: "not-json"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := NewMemoryStorage("http://localhost:4566")
			topicARN := newPlainTopic(t, storage)

			result, err := storage.Publish(ctx, topicARN, "hello", "", "", "", "", tc.attrs)
			expectTopicErrorCode(t, err, errInvalidParameter)

			if result != nil {
				t.Errorf("Publish() result = %+v, want nil", result)
			}
		})
	}
}

func TestPublish_MessageAttributeAcceptedTypes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	tests := []struct {
		name  string
		attrs map[string]MessageAttribute
	}{
		{name: "string", attrs: map[string]MessageAttribute{"a": {DataType: "String", StringValue: "v"}}},
		{name: "number", attrs: map[string]MessageAttribute{"a": {DataType: "Number", StringValue: "12.5"}}},
		{name: "binary", attrs: map[string]MessageAttribute{"a": {DataType: "Binary", BinaryValue: []byte{1, 2}}}},
		{name: "custom string", attrs: map[string]MessageAttribute{"a": {DataType: "String.custom", StringValue: "v"}}},
		{name: "string array", attrs: map[string]MessageAttribute{"a": {DataType: "String.Array", StringValue: `["x","y"]`}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			storage := NewMemoryStorage("http://localhost:4566")
			topicARN := newPlainTopic(t, storage)

			if _, err := storage.Publish(ctx, topicARN, "hello", "", "", "", "", tc.attrs); err != nil {
				t.Fatalf("Publish() error = %v", err)
			}
		})
	}
}

func TestPublish_MessageTooLong(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	topicARN := newPlainTopic(t, storage)

	// Body alone just under the limit is fine.
	if _, err := storage.Publish(context.Background(), topicARN, strings.Repeat("x", maxPublishMessageSize), "", "", "", "", nil); err != nil {
		t.Fatalf("Publish(256KiB) error = %v", err)
	}

	// One byte over is rejected.
	result, err := storage.Publish(context.Background(), topicARN, strings.Repeat("x", maxPublishMessageSize+1), "", "", "", "", nil)
	expectTopicErrorCode(t, err, errInvalidParameter)

	if result != nil {
		t.Errorf("Publish() result = %+v, want nil", result)
	}

	// Attributes count toward the limit too.
	result, err = storage.Publish(context.Background(), topicARN, "hello", "", "", "", "",
		map[string]MessageAttribute{"big": {DataType: "String", StringValue: strings.Repeat("x", maxPublishMessageSize)}})
	expectTopicErrorCode(t, err, errInvalidParameter)

	if result != nil {
		t.Errorf("Publish() result = %+v, want nil", result)
	}
}
