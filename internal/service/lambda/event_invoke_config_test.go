package lambda

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func newEventInvokeConfigStorage(t *testing.T) *MemoryStorage {
	t.Helper()

	storage := NewMemoryStorage("http://localhost:4566")

	_, err := storage.CreateFunction(t.Context(), &CreateFunctionRequest{
		FunctionName: "eic-fn",
		Role:         "arn:aws:iam::000000000000:role/test-role",
	})
	if err != nil {
		t.Fatalf("CreateFunction: %v", err)
	}

	return storage
}

func TestFunctionEventInvokeConfigDefaults(t *testing.T) {
	storage := newEventInvokeConfigStorage(t)

	// AWS reports the default config (2 retries, 21600s event age) even
	// before PutFunctionEventInvokeConfig.
	config, err := storage.GetFunctionEventInvokeConfig(t.Context(), "eic-fn")
	if err != nil {
		t.Fatalf("GetFunctionEventInvokeConfig: %v", err)
	}

	if *config.MaximumRetryAttempts != 2 || *config.MaximumEventAgeInSeconds != 21600 {
		t.Errorf("unexpected defaults: %+v", config)
	}

	configs, err := storage.ListFunctionEventInvokeConfigs(t.Context(), "eic-fn")
	if err != nil {
		t.Fatalf("ListFunctionEventInvokeConfigs: %v", err)
	}

	if len(configs) != 0 {
		t.Errorf("expected empty list before Put, got %+v", configs)
	}
}

func TestFunctionEventInvokeConfigPutGetDelete(t *testing.T) {
	storage := newEventInvokeConfigStorage(t)
	ctx := t.Context()

	retries := 0
	age := 120

	config, err := storage.PutFunctionEventInvokeConfig(ctx, "eic-fn", &functionEventInvokeConfigRequest{
		MaximumRetryAttempts:     &retries,
		MaximumEventAgeInSeconds: &age,
		DestinationConfig: &DestinationConfig{
			OnFailure: &EventInvokeDestination{Destination: "arn:aws:sqs:us-east-1:000000000000:dlq"},
		},
	})
	if err != nil {
		t.Fatalf("PutFunctionEventInvokeConfig: %v", err)
	}

	if *config.MaximumRetryAttempts != 0 || *config.MaximumEventAgeInSeconds != 120 {
		t.Errorf("unexpected config: %+v", config)
	}

	got, err := storage.GetFunctionEventInvokeConfig(ctx, "eic-fn")
	if err != nil {
		t.Fatalf("GetFunctionEventInvokeConfig: %v", err)
	}

	if got.DestinationConfig == nil || got.DestinationConfig.OnFailure.Destination != "arn:aws:sqs:us-east-1:000000000000:dlq" {
		t.Errorf("destination not round-tripped: %+v", got.DestinationConfig)
	}

	configs, err := storage.ListFunctionEventInvokeConfigs(ctx, "eic-fn")
	if err != nil {
		t.Fatalf("ListFunctionEventInvokeConfigs: %v", err)
	}

	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}

	if err := storage.DeleteFunctionEventInvokeConfig(ctx, "eic-fn"); err != nil {
		t.Fatalf("DeleteFunctionEventInvokeConfig: %v", err)
	}

	configs, _ = storage.ListFunctionEventInvokeConfigs(ctx, "eic-fn")
	if len(configs) != 0 {
		t.Errorf("expected empty list after Delete, got %+v", configs)
	}

	// Defaults restored after delete.
	got, _ = storage.GetFunctionEventInvokeConfig(ctx, "eic-fn")
	if *got.MaximumRetryAttempts != 2 {
		t.Errorf("expected default retries after delete, got %d", *got.MaximumRetryAttempts)
	}
}

func TestFunctionEventInvokeConfigValidation(t *testing.T) {
	storage := newEventInvokeConfigStorage(t)
	ctx := t.Context()

	badRetries := 3
	if _, err := storage.PutFunctionEventInvokeConfig(ctx, "eic-fn", &functionEventInvokeConfigRequest{
		MaximumRetryAttempts: &badRetries,
	}); err == nil {
		t.Error("expected error for MaximumRetryAttempts=3")
	}

	badAge := 30
	if _, err := storage.PutFunctionEventInvokeConfig(ctx, "eic-fn", &functionEventInvokeConfigRequest{
		MaximumEventAgeInSeconds: &badAge,
	}); err == nil {
		t.Error("expected error for MaximumEventAgeInSeconds=30")
	}

	if _, err := storage.PutFunctionEventInvokeConfig(ctx, "eic-fn", &functionEventInvokeConfigRequest{
		DestinationConfig: &DestinationConfig{
			OnFailure: &EventInvokeDestination{Destination: "not-an-arn"},
		},
	}); err == nil {
		t.Error("expected error for malformed destination ARN")
	}

	if _, err := storage.PutFunctionEventInvokeConfig(ctx, "eic-fn", &functionEventInvokeConfigRequest{
		DestinationConfig: &DestinationConfig{
			OnSuccess: &EventInvokeDestination{Destination: "arn:aws:s3:::bucket"},
		},
	}); err == nil {
		t.Error("expected error for unsupported destination service")
	}

	if _, err := storage.PutFunctionEventInvokeConfig(ctx, "missing-fn", &functionEventInvokeConfigRequest{}); err == nil {
		t.Error("expected error for unknown function")
	}
}

func TestFunctionEventInvokeConfigRemovedWithFunction(t *testing.T) {
	storage := newEventInvokeConfigStorage(t)
	ctx := t.Context()

	retries := 1
	if _, err := storage.PutFunctionEventInvokeConfig(ctx, "eic-fn", &functionEventInvokeConfigRequest{
		MaximumRetryAttempts: &retries,
	}); err != nil {
		t.Fatalf("PutFunctionEventInvokeConfig: %v", err)
	}

	if err := storage.DeleteFunction(ctx, "eic-fn"); err != nil {
		t.Fatalf("DeleteFunction: %v", err)
	}

	storage.mu.RLock()
	defer storage.mu.RUnlock()

	if _, ok := storage.EventInvokeConfigs["eic-fn"]; ok {
		t.Error("invoke config left behind after function deletion")
	}
}

// TestAsyncDispatcherHonorsConfiguredRetries verifies a function whose
// FunctionEventInvokeConfig sets MaximumRetryAttempts=0 gets no retries on
// function errors (AWS retries twice by default).
func TestAsyncDispatcherHonorsConfiguredRetries(t *testing.T) {
	d := newTestDispatcher(t)

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	zero := 0
	d.invokeConfigOf = func(string) *FunctionEventInvokeConfig {
		return &FunctionEventInvokeConfig{MaximumRetryAttempts: &zero}
	}

	deliverer := &endpointDeliverer{client: d.client, endpoint: server.URL, gate: d.gate("fn")}
	d.enqueue("fn", deliverer, []byte(`{}`))

	// Give the dispatcher room to (incorrectly) retry.
	time.Sleep(300 * time.Millisecond)

	if n := attempts.Load(); n != 1 {
		t.Fatalf("expected exactly 1 attempt with MaximumRetryAttempts=0, got %d", n)
	}
}

// TestAsyncDispatcherDefaultRetriesUnchanged pins the AWS default of two
// retries when no invoke config overrides it.
func TestAsyncDispatcherDefaultRetriesUnchanged(t *testing.T) {
	d := newTestDispatcher(t)

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	deliverer := &endpointDeliverer{client: d.client, endpoint: server.URL, gate: d.gate("fn")}
	d.enqueue("fn", deliverer, []byte(`{}`))

	if !waitFor(t, 5*time.Second, func() bool { return attempts.Load() >= 3 }) {
		t.Fatalf("expected initial attempt + 2 retries, got %d", attempts.Load())
	}
}
