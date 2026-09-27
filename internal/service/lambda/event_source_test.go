package lambda

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeSQSSource implements SQSEventSource, draining a canned batch once and
// then returning empty receives.
type fakeSQSSource struct {
	mu        sync.Mutex
	messages  []*SQSPollerMessage
	deleted   []string
	receives  int
	errOnPoll bool
}

func (f *fakeSQSSource) ReceiveMessages(_ context.Context, _ string, _, _ int) ([]*SQSPollerMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.receives++

	if f.errOnPoll {
		return nil, errFakePoll
	}

	batch := f.messages
	f.messages = nil

	return batch, nil
}

func (f *fakeSQSSource) DeleteMessage(_ context.Context, _ string, receiptHandle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deleted = append(f.deleted, receiptHandle)

	return nil
}

var errFakePoll = errors.New("poll failed")

func newEventSourceTestService(t *testing.T, handler http.Handler) (*Service, *fakeSQSSource) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	storage := NewMemoryStorage("http://localhost:4566")
	svc := New(storage, "http://localhost:4566")
	t.Cleanup(func() { svc.Close() })

	_, err := storage.CreateFunction(t.Context(), &CreateFunctionRequest{
		FunctionName:   "esm-fn",
		Role:           "arn:aws:iam::000000000000:role/test-role",
		InvokeEndpoint: server.URL,
	})
	if err != nil {
		t.Fatalf("CreateFunction: %v", err)
	}

	source := &fakeSQSSource{}
	svc.SetSQSEventSource(source)

	return svc, source
}

func newMapping(t *testing.T, svc *Service, enabled *bool) *EventSourceMapping {
	t.Helper()

	mapping, err := svc.storage.CreateEventSourceMapping(t.Context(), &CreateEventSourceMappingRequest{
		FunctionName:   "esm-fn",
		EventSourceArn: "arn:aws:sqs:us-east-1:000000000000:q1",
		Enabled:        enabled,
	})
	if err != nil {
		t.Fatalf("CreateEventSourceMapping: %v", err)
	}

	return mapping
}

func TestSQSEventSourcePollerInvokesFunctionAndDeletesMessages(t *testing.T) {
	recorder := &payloadRecorder{}
	svc, source := newEventSourceTestService(t, recorder)

	source.messages = []*SQSPollerMessage{
		{
			MessageID:     "m-1",
			ReceiptHandle: "rh-1",
			Body:          `{"hello":"world"}`,
			MD5OfBody:     "md5",
			Attributes:    map[string]string{"SentTimestamp": "1700000000000"},
			MessageAttributes: map[string]SQSPollerMessageAttribute{
				"attr": {DataType: "String", StringValue: "v"},
			},
		},
	}

	svc.syncEventSourcePoller(newMapping(t, svc, nil))
	defer svc.stopAllEventSourcePollers()

	if !waitFor(t, 5*time.Second, func() bool { return len(recorder.snapshot()) > 0 }) {
		t.Fatal("timed out waiting for function invocation")
	}

	var event struct {
		Records []sqsEventRecord `json:"Records"`
	}

	if err := json.Unmarshal([]byte(recorder.snapshot()[0]), &event); err != nil {
		t.Fatalf("invalid SQS event payload: %v", err)
	}

	if len(event.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(event.Records))
	}

	rec := event.Records[0]
	if rec.MessageID != "m-1" || rec.Body != `{"hello":"world"}` {
		t.Errorf("unexpected record: %+v", rec)
	}

	if rec.EventSourceARN != "arn:aws:sqs:us-east-1:000000000000:q1" {
		t.Errorf("unexpected eventSourceARN: %s", rec.EventSourceARN)
	}

	if rec.EventSource != "aws:sqs" || rec.AWSRegion != "us-east-1" {
		t.Errorf("unexpected record metadata: %+v", rec)
	}

	attr := rec.MessageAttributes["attr"]
	if attr.StringValue == nil || *attr.StringValue != "v" {
		t.Errorf("message attribute not propagated: %+v", rec.MessageAttributes)
	}

	if !waitFor(t, 3*time.Second, func() bool {
		source.mu.Lock()
		defer source.mu.Unlock()

		return len(source.deleted) > 0
	}) {
		t.Fatal("message was not deleted after successful invocation")
	}
}

func TestSQSEventSourcePollerDisabledByMapping(t *testing.T) {
	recorder := &payloadRecorder{}
	svc, source := newEventSourceTestService(t, recorder)

	source.messages = []*SQSPollerMessage{{MessageID: "m-1", ReceiptHandle: "rh-1", Body: "x"}}

	disabled := false

	svc.syncEventSourcePoller(newMapping(t, svc, &disabled))
	defer svc.stopAllEventSourcePollers()

	time.Sleep(200 * time.Millisecond)

	if got := recorder.snapshot(); len(got) != 0 {
		t.Fatalf("disabled mapping must not invoke the function, got %v", got)
	}
}

func TestSQSEventSourcePollerStopsOnDelete(t *testing.T) {
	svc, source := newEventSourceTestService(t, &payloadRecorder{})

	mapping := newMapping(t, svc, nil)

	svc.syncEventSourcePoller(mapping)
	svc.stopEventSourcePoller(mapping.UUID)

	source.mu.Lock()
	receives := source.receives
	source.mu.Unlock()

	time.Sleep(100 * time.Millisecond)

	source.mu.Lock()
	defer source.mu.Unlock()

	if source.receives > receives {
		t.Fatalf("poller still running after stop: %d -> %d", receives, source.receives)
	}
}

func TestSQSEventSourcePollerKeepsPollingOnError(t *testing.T) {
	recorder := &payloadRecorder{}
	svc, source := newEventSourceTestService(t, recorder)

	source.errOnPoll = true

	mapping := newMapping(t, svc, nil)

	svc.syncEventSourcePoller(mapping)
	defer svc.stopEventSourcePoller(mapping.UUID)

	// Wait until the poller has retried at least once, then let it succeed.
	if !waitFor(t, 3*time.Second, func() bool {
		source.mu.Lock()
		defer source.mu.Unlock()

		return source.receives >= 2
	}) {
		t.Fatal("poller did not retry after receive error")
	}

	source.mu.Lock()
	source.errOnPoll = false
	source.messages = []*SQSPollerMessage{{MessageID: "m-9", ReceiptHandle: "rh-9", Body: "x"}}
	source.mu.Unlock()

	if !waitFor(t, 5*time.Second, func() bool { return len(recorder.snapshot()) > 0 }) {
		t.Fatal("poller did not recover from transient receive error")
	}
}

func TestSQSEventSourcePollerKeepsMessagesOnInvokeFailure(t *testing.T) {
	failing := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	svc, source := newEventSourceTestService(t, failing)

	source.messages = []*SQSPollerMessage{{MessageID: "m-1", ReceiptHandle: "rh-1", Body: "x"}}

	mapping := newMapping(t, svc, nil)

	svc.syncEventSourcePoller(mapping)
	defer svc.stopEventSourcePoller(mapping.UUID)

	// Wait for at least one (failed) invocation attempt.
	if !waitFor(t, 5*time.Second, func() bool {
		source.mu.Lock()
		defer source.mu.Unlock()

		return source.receives >= 1
	}) {
		t.Fatal("poller never received messages")
	}

	time.Sleep(200 * time.Millisecond)

	source.mu.Lock()
	defer source.mu.Unlock()

	if len(source.deleted) != 0 {
		t.Fatalf("message deleted despite failed invocation: %v", source.deleted)
	}
}
