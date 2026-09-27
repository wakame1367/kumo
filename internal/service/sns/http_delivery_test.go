package sns

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// recordingPoster is a fake HTTPPoster that records posted envelopes.
type recordingPoster struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (r *recordingPoster) PostJSON(_ context.Context, _ string, payload []byte) error {
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloads = append(r.payloads, m)

	return nil
}

func (r *recordingPoster) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.payloads)
}

// waitForPosts polls until n envelopes have been posted or the deadline
// passes, accommodating the asynchronous SubscriptionConfirmation POST.
func waitForPosts(t *testing.T, p *recordingPoster, n int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p.count() >= n {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("posted %d envelopes, want %d", p.count(), n)
}

func TestSubscribe_HTTPSendsConfirmationAndStaysPending(t *testing.T) {
	t.Parallel()

	poster := &recordingPoster{}
	storage := NewMemoryStorage("http://localhost:4566")
	storage.SetHTTPPoster(poster)

	ctx := context.Background()

	topic, err := storage.CreateTopic(ctx, "t", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	sub, err := storage.Subscribe(ctx, topic.ARN, protocolHTTP, "http://localhost:9999/hook", nil)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if sub.Confirmed || sub.ConfirmationToken == "" {
		t.Fatalf("subscription = %+v, want pending with a token", sub)
	}

	waitForPosts(t, poster, 1)

	if poster.payloads[0]["Type"] != "SubscriptionConfirmation" ||
		poster.payloads[0]["Token"] != sub.ConfirmationToken {
		t.Errorf("confirmation = %v", poster.payloads[0])
	}
}

func TestPublish_HTTPDeliveredOnlyAfterConfirmation(t *testing.T) {
	t.Parallel()

	poster := &recordingPoster{}
	storage := NewMemoryStorage("http://localhost:4566")
	storage.SetHTTPPoster(poster)

	ctx := context.Background()

	topic, err := storage.CreateTopic(ctx, "t", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	sub, err := storage.Subscribe(ctx, topic.ARN, protocolHTTPS, "https://localhost:9999/hook", nil)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	// Pending: publish delivers nothing.
	if _, err := storage.Publish(ctx, topic.ARN, "hello", "", "subj", "", "", nil); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	waitForPosts(t, poster, 1) // only the confirmation

	if _, err := storage.ConfirmSubscription(ctx, topic.ARN, sub.ConfirmationToken); err != nil {
		t.Fatalf("ConfirmSubscription() error = %v", err)
	}

	if _, err := storage.Publish(ctx, topic.ARN, "hello", "", "subj", "", "", nil); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	waitForPosts(t, poster, 2)

	got := poster.payloads[1]
	if got["Type"] != "Notification" || got["Message"] != "hello" || got["Subject"] != "subj" {
		t.Errorf("notification = %v", got)
	}
}

func TestConfirmSubscription_Errors(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	storage.SetHTTPPoster(nil)

	ctx := context.Background()

	if _, err := storage.ConfirmSubscription(ctx, "arn:aws:sns:us-east-1:000000000000:missing", "tok"); err == nil {
		t.Error("ConfirmSubscription(missing topic) want error")
	}

	topic, err := storage.CreateTopic(ctx, "t", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	if _, err := storage.ConfirmSubscription(ctx, topic.ARN, "wrong-token"); err == nil {
		t.Error("ConfirmSubscription(bad token) want error")
	}
}

func TestSubscribe_RequiresEndpoint(t *testing.T) {
	t.Parallel()

	storage := NewMemoryStorage("http://localhost:4566")
	storage.SetHTTPPoster(nil)

	ctx := context.Background()

	topic, err := storage.CreateTopic(ctx, "t", nil, nil)
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	if _, err := storage.Subscribe(ctx, topic.ARN, protocolHTTP, "", nil); err == nil {
		t.Error("Subscribe(empty endpoint) want error")
	}
}
