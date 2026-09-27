package eventbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newRuleWithTarget(t *testing.T, s *MemoryStorage, target TargetInput) {
	t.Helper()

	ctx := context.Background()

	if _, err := s.PutRule(ctx, &PutRuleRequest{
		Name:         "test-rule",
		EventPattern: `{"source":["my.test"]}`,
		State:        string(RuleStateEnabled),
	}); err != nil {
		t.Fatalf("PutRule: %v", err)
	}

	failed, err := s.PutTargets(ctx, "", "test-rule", []TargetInput{target})
	if err != nil {
		t.Fatalf("PutTargets: %v", err)
	}

	if len(failed) != 0 {
		t.Fatalf("PutTargets failed entries: %+v", failed)
	}
}

// TestPutEvents_TargetInputConstant verifies that a target with Input set
// receives the constant JSON verbatim instead of the event envelope.
func TestPutEvents_TargetInputConstant(t *testing.T) {
	t.Parallel()

	received := make(chan []byte, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	s := NewMemoryStorage()
	ctx := context.Background()

	conn, err := s.CreateConnection(ctx, &CreateConnectionRequest{
		Name:              "c",
		AuthorizationType: "API_KEY",
		AuthParameters: AuthParameters{
			APIKeyAuthParameters: &APIKeyAuthParameters{APIKeyName: "k", APIKeyValue: "v"},
		},
	})
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}

	dest, err := s.CreateAPIDestination(ctx, &CreateAPIDestinationRequest{
		Name:               "d",
		ConnectionArn:      conn.Arn,
		InvocationEndpoint: server.URL,
		HTTPMethod:         http.MethodPost,
	})
	if err != nil {
		t.Fatalf("CreateAPIDestination: %v", err)
	}

	newRuleWithTarget(t, s, TargetInput{
		ID:    "t1",
		Arn:   dest.Arn,
		Input: `{"constant":"payload"}`,
	})

	if _, err := s.PutEvents(ctx, []PutEventsRequestEntry{
		{Source: "my.test", DetailType: "TestEvent", Detail: `{"hello":"world"}`},
	}); err != nil {
		t.Fatalf("PutEvents: %v", err)
	}

	select {
	case body := <-received:
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("delivered payload not valid JSON: %v", err)
		}

		if got["constant"] != "payload" || len(got) != 1 {
			t.Errorf("expected constant input verbatim, got %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delivery")
	}
}

// TestPutTargets_InputValidation covers the mutually exclusive input
// parameters and constant-Input JSON validation, reported as per-entry
// failures like AWS.
func TestPutTargets_InputValidation(t *testing.T) {
	t.Parallel()

	s := NewMemoryStorage()
	ctx := context.Background()

	if _, err := s.PutRule(ctx, &PutRuleRequest{
		Name:         "r",
		EventPattern: `{"source":["x"]}`,
		State:        string(RuleStateEnabled),
	}); err != nil {
		t.Fatalf("PutRule: %v", err)
	}

	cases := []struct {
		name   string
		target TargetInput
	}{
		{"missing id", TargetInput{Arn: "arn:aws:sqs:us-east-1:000000000000:q"}},
		{"missing arn", TargetInput{ID: "t"}},
		{"input plus inputpath", TargetInput{
			ID: "t", Arn: "arn:aws:sqs:us-east-1:000000000000:q",
			Input: `{}`, InputPath: "$.detail",
		}},
		{"input plus transformer", TargetInput{
			ID: "t", Arn: "arn:aws:sqs:us-east-1:000000000000:q",
			Input: `{}`,
			InputTransformer: &InputTransformerInput{
				InputPathsMap: map[string]string{"s": "$.source"},
				InputTemplate: `{"s":<s>}`,
			},
		}},
		{"invalid input json", TargetInput{
			ID: "t", Arn: "arn:aws:sqs:us-east-1:000000000000:q", Input: "not json",
		}},
		{"empty input template", TargetInput{
			ID: "t", Arn: "arn:aws:sqs:us-east-1:000000000000:q",
			InputTransformer: &InputTransformerInput{InputPathsMap: map[string]string{"s": "$.source"}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failed, err := s.PutTargets(ctx, "", "r", []TargetInput{tc.target})
			if err != nil {
				t.Fatalf("PutTargets: %v", err)
			}

			if len(failed) != 1 || failed[0].ErrorCode != errInvalidParameter {
				t.Fatalf("expected one ValidationException failed entry, got %+v", failed)
			}
		})
	}
}

// TestPutEvents_EntryValidation exercises the per-entry failure results AWS
// returns for malformed or unroutable entries.
func TestPutEvents_EntryValidation(t *testing.T) {
	t.Parallel()

	s := NewMemoryStorage()
	ctx := context.Background()

	results, err := s.PutEvents(ctx, []PutEventsRequestEntry{
		{Source: "ok", DetailType: "T", Detail: "{}"},
		{DetailType: "T", Detail: "{}"},              // missing Source
		{Source: "x", Detail: "{}"},                  // missing DetailType
		{Source: "x", DetailType: "T", Detail: "!!"}, // malformed Detail
		{Source: "x", DetailType: "T", Detail: "{}", EventBusName: "no-such-bus"},
	})
	if err != nil {
		t.Fatalf("PutEvents: %v", err)
	}

	if results[0].EventID == "" || results[0].ErrorCode != "" {
		t.Errorf("valid entry failed: %+v", results[0])
	}

	if results[1].ErrorCode != "InvalidArgument" {
		t.Errorf("missing Source: %+v", results[1])
	}

	if results[2].ErrorCode != "InvalidArgument" {
		t.Errorf("missing DetailType: %+v", results[2])
	}

	if results[3].ErrorCode != "MalformedDetail" {
		t.Errorf("malformed detail: %+v", results[3])
	}

	if results[4].ErrorCode != errEventBusNotFound {
		t.Errorf("unknown bus: %+v", results[4])
	}
}

// TestPutEvents_TooManyEntries covers the >10 entries request rejection.
func TestPutEvents_TooManyEntries(t *testing.T) {
	t.Parallel()

	s := NewMemoryStorage()

	entries := make([]PutEventsRequestEntry, 11)
	for i := range entries {
		entries[i] = PutEventsRequestEntry{Source: "s", DetailType: "T", Detail: "{}"}
	}

	_, err := s.PutEvents(context.Background(), entries)
	if err == nil {
		t.Fatal("expected error for >10 entries")
	}
}

// TestPutEvents_SNSTarget verifies events are published to SNS targets via
// the Query-protocol Publish action.
func TestPutEvents_SNSTarget(t *testing.T) {
	t.Parallel()

	received := make(chan url.Values, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		received <- form
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	s := NewMemoryStorage(WithBaseURL(server.URL))
	ctx := context.Background()

	newRuleWithTarget(t, s, TargetInput{
		ID:  "t1",
		Arn: "arn:aws:sns:us-east-1:000000000000:topic1",
	})

	if _, err := s.PutEvents(ctx, []PutEventsRequestEntry{
		{Source: "my.test", DetailType: "TestEvent", Detail: `{"hello":"world"}`},
	}); err != nil {
		t.Fatalf("PutEvents: %v", err)
	}

	select {
	case form := <-received:
		if form.Get("Action") != "Publish" {
			t.Errorf("expected Action=Publish, got %q", form.Get("Action"))
		}

		if form.Get("TopicArn") != "arn:aws:sns:us-east-1:000000000000:topic1" {
			t.Errorf("unexpected TopicArn: %q", form.Get("TopicArn"))
		}

		msg := form.Get("Message")
		if !strings.Contains(msg, `"hello":"world"`) {
			t.Errorf("event payload not in SNS message: %q", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SNS delivery")
	}
}
