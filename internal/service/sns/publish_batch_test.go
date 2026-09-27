package sns

import (
	"encoding/json"
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"
)

// doPublishBatch dispatches a JSON-protocol PublishBatch request through the
// service and returns the HTTP status plus decoded response.
func doPublishBatch(t *testing.T, storage Storage, body string) (int, XMLPublishBatchResponse) {
	t.Helper()

	svc := New(storage)

	req := httptest.NewRequestWithContext(t.Context(), "POST", "/", strings.NewReader(body))
	req.Header.Set("X-Amz-Target", "AmazonSimpleNotificationService.PublishBatch")
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")

	rec := httptest.NewRecorder()
	svc.DispatchAction(rec, req)

	var resp XMLPublishBatchResponse
	if rec.Code == 200 {
		if err := xml.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response is not XML: %v\n%s", err, rec.Body.String())
		}
	}

	return rec.Code, resp
}

func TestPublishBatch_AllSucceed(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	topicARN := newPlainTopic(t, storage)

	body, _ := json.Marshal(PublishBatchRequest{
		TopicARN: topicARN,
		PublishBatchRequestEntries: []PublishBatchRequestEntry{
			{ID: "m1", Message: testHelloMessage},
			{ID: "m2", Message: "world", Subject: "s"},
		},
	})

	status, resp := doPublishBatch(t, storage, string(body))
	if status != 200 {
		t.Fatalf("status = %d", status)
	}

	if len(resp.PublishBatchResult.Successful.Member) != 2 || len(resp.PublishBatchResult.Failed.Member) != 0 {
		t.Fatalf("result = %+v", resp.PublishBatchResult)
	}

	for i, entry := range resp.PublishBatchResult.Successful.Member {
		if entry.ID != []string{"m1", "m2"}[i] || entry.MessageID == "" {
			t.Errorf("entry = %+v", entry)
		}
	}
}

func TestPublishBatch_PerEntryFailures(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	topicARN := newFIFOTopic(t, storage, nil)

	// FIFO topic: entry lacking MessageGroupId fails while the rest succeed.
	body, _ := json.Marshal(PublishBatchRequest{
		TopicARN: topicARN,
		PublishBatchRequestEntries: []PublishBatchRequestEntry{
			{ID: "ok", Message: "m", MessageGroupID: "g", MessageDeduplicationID: "d1"},
			{ID: "bad", Message: "m", MessageDeduplicationID: "d2"},
		},
	})

	status, resp := doPublishBatch(t, storage, string(body))
	if status != 200 {
		t.Fatalf("status = %d", status)
	}

	if len(resp.PublishBatchResult.Successful.Member) != 1 ||
		resp.PublishBatchResult.Successful.Member[0].ID != "ok" ||
		resp.PublishBatchResult.Successful.Member[0].SequenceNumber == "" {
		t.Fatalf("successful = %+v", resp.PublishBatchResult.Successful)
	}

	if len(resp.PublishBatchResult.Failed.Member) != 1 {
		t.Fatalf("failed = %+v", resp.PublishBatchResult.Failed)
	}

	fail := resp.PublishBatchResult.Failed.Member[0]
	if fail.ID != "bad" || !fail.SenderFault || fail.Code != errInvalidParameter {
		t.Errorf("failed entry = %+v", fail)
	}
}

func TestPublishBatch_RequestValidation(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	topicARN := newPlainTopic(t, storage)

	entry := PublishBatchRequestEntry{ID: "m1", Message: testHelloMessage}

	tests := []struct {
		name    string
		topic   string
		entries []PublishBatchRequestEntry
	}{
		{name: "empty batch", topic: topicARN, entries: nil},
		{name: "too many entries", topic: topicARN, entries: make([]PublishBatchRequestEntry, 11)},
		{name: "duplicate ids", topic: topicARN, entries: []PublishBatchRequestEntry{entry, entry}},
		{name: "invalid id", topic: topicARN, entries: []PublishBatchRequestEntry{{ID: "bad id!", Message: "m"}}},
		{name: "missing topic arn", topic: "", entries: []PublishBatchRequestEntry{entry}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body, _ := json.Marshal(PublishBatchRequest{
				TopicARN:                   tc.topic,
				PublishBatchRequestEntries: tc.entries,
			})

			status, _ := doPublishBatch(t, storage, string(body))
			if status == 200 {
				t.Error("want error status")
			}
		})
	}
}

func TestPublishBatch_MissingTopicFailsRequest(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")

	body, _ := json.Marshal(PublishBatchRequest{
		TopicARN: "arn:aws:sns:us-east-1:000000000000:missing",
		PublishBatchRequestEntries: []PublishBatchRequestEntry{
			{ID: "m1", Message: testHelloMessage},
		},
	})

	status, _ := doPublishBatch(t, storage, string(body))
	if status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
}
