package cloudfront

import (
	"context"
	"testing"
)

func TestManagedCachePolicies_ReadOnly(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	got, err := store.GetCachePolicy(ctx, managedCachingOptimizedID)
	if err != nil {
		t.Fatalf("GetCachePolicy(managed): %v", err)
	}

	if got.Config.Name != "Managed-CachingOptimized" || *got.Config.MinTTL != 1 || *got.Config.DefaultTTL != 86400 || !*got.Config.Parameters.EnableAcceptEncodingBrotli {
		t.Errorf("Managed-CachingOptimized = %+v", got.Config)
	}

	// Callers must not be able to alter the shared managed definitions.
	*got.Config.MinTTL = 99

	again, _ := store.GetCachePolicy(ctx, managedCachingOptimizedID)
	if *again.Config.MinTTL != 1 {
		t.Errorf("managed policy mutated through a returned copy")
	}

	if _, err := store.UpdateCachePolicy(ctx, managedCachingOptimizedID, validCachePolicy("x"), managedPolicyETag); errorCode(t, err) != errIllegalUpdate {
		t.Errorf("update managed: err = %v", err)
	}

	if err := store.DeleteCachePolicy(ctx, managedCachingOptimizedID, managedPolicyETag); errorCode(t, err) != errIllegalDelete {
		t.Errorf("delete managed: err = %v", err)
	}
}

func TestManagedPolicySpecs_AreReferenceable(t *testing.T) {
	t.Parallel()

	for _, spec := range managedCachePolicySpecs {
		if _, ok := managedCachePolicies[spec.id]; !ok {
			t.Errorf("readable managed cache policy %s is not referenceable", spec.id)
		}

		if err := validateCachePolicyConfig(&spec.policy().Config); err != nil {
			t.Errorf("managed cache policy %s is invalid: %v", spec.id, err)
		}
	}

	for _, spec := range managedOriginRequestPolicySpecs {
		if _, ok := managedOriginRequestPolicies[spec.id]; !ok {
			t.Errorf("readable managed origin request policy %s is not referenceable", spec.id)
		}

		if err := validateOriginRequestPolicyConfig(&spec.policy().Config); err != nil {
			t.Errorf("managed origin request policy %s is invalid: %v", spec.id, err)
		}
	}
}
