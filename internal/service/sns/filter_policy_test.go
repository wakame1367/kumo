package sns

import (
	"context"
	"testing"
)

// subWithPolicy returns a subscription carrying the given attributes for
// filter-policy evaluation.
func subWithPolicy(attrs map[string]string) *Subscription {
	return &Subscription{
		ARN:                    "arn:aws:sns:us-east-1:000000000000:t:sub",
		TopicARN:               "arn:aws:sns:us-east-1:000000000000:t",
		Protocol:               protocolSQS,
		Endpoint:               "arn:aws:sqs:us-east-1:000000000000:q",
		SubscriptionAttributes: attrs,
	}
}

func TestEvaluateFilterPolicy_AttributeOperators(t *testing.T) {
	t.Parallel()

	attrs := map[string]MessageAttribute{
		"store":  {DataType: dataTypeString, StringValue: "example-corp"},
		"event":  {DataType: dataTypeString, StringValue: "order_cancelled"},
		"price":  {DataType: "Number", StringValue: "150"},
		"source": {DataType: dataTypeString, StringValue: "10.0.1.5"},
	}

	tests := []struct {
		name   string
		policy string
		want   bool
	}{
		{"exact match", `{"store": ["example-corp"]}`, true},
		{"exact miss", `{"store": ["other"]}`, false},
		{"or list", `{"store": ["a", "example-corp", "b"]}`, true},
		{"exists true", `{"store": [{"exists": true}]}`, true},
		{"exists false on missing", `{"missing": [{"exists": false}]}`, true},
		{"exists true on missing", `{"missing": [{"exists": true}]}`, false},
		{"prefix", `{"store": [{"prefix": "example"}]}`, true},
		{"prefix miss", `{"store": [{"prefix": "corp"}]}`, false},
		{"suffix", `{"event": [{"suffix": "cancelled"}]}`, true},
		{"suffix miss", `{"event": [{"suffix": "created"}]}`, false},
		{"equals-ignore-case", `{"store": [{"equals-ignore-case": "EXAMPLE-CORP"}]}`, true},
		{"anything-but string", `{"store": [{"anything-but": "other"}]}`, true},
		{"anything-but deny", `{"store": [{"anything-but": ["example-corp"]}]}`, false},
		{"anything-but nested prefix", `{"store": [{"anything-but": {"prefix": "ex"}}]}`, false},
		{"anything-but nested prefix miss", `{"store": [{"anything-but": {"prefix": "zz"}}]}`, true},
		{"anything-but nested ignore-case", `{"store": [{"anything-but": {"equals-ignore-case": "EXAMPLE-CORP"}}]}`, false},
		{"numeric equals", `{"price": [{"numeric": ["=", 150]}]}`, true},
		{"numeric range", `{"price": [{"numeric": [">", 100, "<=", 200]}]}`, true},
		{"numeric out of range", `{"price": [{"numeric": ["<", 100]}]}`, false},
		{"numeric on missing", `{"missing": [{"numeric": [">", 0]}]}`, false},
		{"cidr match", `{"source": [{"cidr": "10.0.0.0/16"}]}`, true},
		{"cidr miss", `{"source": [{"cidr": "10.0.2.0/24"}]}`, false},
		{"and across keys", `{"store": ["example-corp"], "event": [{"suffix": "cancelled"}]}`, true},
		{"and fails on one key", `{"store": ["example-corp"], "event": ["created"]}`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sub := subWithPolicy(map[string]string{"FilterPolicy": tc.policy})
			if got := evaluateFilterPolicy(sub, "body", attrs); got != tc.want {
				t.Errorf("evaluateFilterPolicy(%s) = %v, want %v", tc.policy, got, tc.want)
			}
		})
	}
}

func TestEvaluateFilterPolicy_MessageBodyScope(t *testing.T) {
	t.Parallel()

	body := `{"event": "order_placed", "detail": {"state": "tx"}, "price": 42, "tags": ["a", "b"]}`
	attrs := map[string]MessageAttribute{}

	tests := []struct {
		name   string
		policy string
		want   bool
	}{
		{"top-level field", `{"event": ["order_placed"]}`, true},
		{"nested field", `{"detail": {"state": ["tx"]}}`, true},
		{"nested miss", `{"detail": {"state": ["ca"]}}`, false},
		{"numeric body field", `{"price": [{"numeric": [">", 40]}]}`, true},
		{"exact number", `{"price": [42]}`, true},
		{"array element", `{"tags": ["b"]}`, true},
		{"exists on body", `{"event": [{"exists": true}]}`, true},
		{"missing field", `{"nope": [{"exists": false}]}`, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sub := subWithPolicy(map[string]string{
				"FilterPolicy":      tc.policy,
				"FilterPolicyScope": filterPolicyScopeBody,
			})
			if got := evaluateFilterPolicy(sub, body, attrs); got != tc.want {
				t.Errorf("evaluateFilterPolicy(%s) = %v, want %v", tc.policy, got, tc.want)
			}
		})
	}
}

func TestEvaluateFilterPolicy_MessageBodyScopeNonJSON(t *testing.T) {
	t.Parallel()

	sub := subWithPolicy(map[string]string{
		"FilterPolicy":      `{"event": ["x"]}`,
		"FilterPolicyScope": filterPolicyScopeBody,
	})

	if evaluateFilterPolicy(sub, "not json", nil) {
		t.Error("non-JSON body must not match a MessageBody policy")
	}
}

func TestSubscribe_FilterPolicyValidation(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	ctx := context.Background()

	topic, err := storage.CreateTopic(ctx, "t", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	endpoint := "arn:aws:sqs:us-east-1:000000000000:q"

	// Malformed FilterPolicy JSON is rejected.
	if _, err := storage.Subscribe(ctx, topic.ARN, protocolSQS, endpoint,
		map[string]string{"FilterPolicy": "not json"}); err == nil {
		t.Error("Subscribe(invalid FilterPolicy) want error")
	}

	// Policy values that are not arrays or objects are rejected.
	if _, err := storage.Subscribe(ctx, topic.ARN, protocolSQS, endpoint,
		map[string]string{"FilterPolicy": `{"k": "v"}`}); err == nil {
		t.Error("Subscribe(scalar FilterPolicy value) want error")
	}

	// Unknown attribute names are rejected.
	if _, err := storage.Subscribe(ctx, topic.ARN, protocolSQS, endpoint,
		map[string]string{"Bogus": "x"}); err == nil {
		t.Error("Subscribe(unknown attribute) want error")
	}

	// Invalid FilterPolicyScope is rejected.
	if _, err := storage.Subscribe(ctx, topic.ARN, protocolSQS, endpoint,
		map[string]string{"FilterPolicyScope": "Everything"}); err == nil {
		t.Error("Subscribe(invalid FilterPolicyScope) want error")
	}

	// A well-formed policy is accepted.
	if _, err := storage.Subscribe(ctx, topic.ARN, protocolSQS, endpoint,
		map[string]string{
			"FilterPolicy":      `{"store": ["example"]}`,
			"FilterPolicyScope": filterPolicyScopeBody,
		}); err != nil {
		t.Errorf("Subscribe(valid policy) error = %v", err)
	}
}

func TestSetSubscriptionAttribute_FilterPolicyValidation(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	ctx := context.Background()

	topic, err := storage.CreateTopic(ctx, "t", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	sub, err := storage.Subscribe(ctx, topic.ARN, protocolSQS, "arn:aws:sqs:us-east-1:000000000000:q", nil)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if err := storage.SetSubscriptionAttribute(ctx, sub.ARN, "FilterPolicy", "not json"); err == nil {
		t.Error("SetSubscriptionAttribute(invalid FilterPolicy) want error")
	}

	if err := storage.SetSubscriptionAttribute(ctx, sub.ARN, "Unknown", "x"); err == nil {
		t.Error("SetSubscriptionAttribute(unknown name) want error")
	}

	if err := storage.SetSubscriptionAttribute(ctx, sub.ARN, "FilterPolicy", `{"k": ["v"]}`); err != nil {
		t.Errorf("SetSubscriptionAttribute(valid) error = %v", err)
	}
}
