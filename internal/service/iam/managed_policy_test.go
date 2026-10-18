package iam

import (
	"context"
	"strings"
	"testing"
)

const lambdaBasicExecutionARN = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"

func TestAWSManagedPolicy_AttachDetach(t *testing.T) {
	t.Parallel()

	store := NewMemoryStorage()
	ctx := context.Background()

	if _, err := store.CreateRole(ctx, &CreateRoleRequest{RoleName: "fn", AssumeRolePolicyDocument: `{"Version":"2012-10-17","Statement":[]}`}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	policy, err := store.GetPolicy(ctx, lambdaBasicExecutionARN)
	if err != nil {
		t.Fatalf("GetPolicy(AWS-managed): %v", err)
	}

	if policy.PolicyName != "AWSLambdaBasicExecutionRole" || policy.Path != "/service-role/" || !strings.Contains(policy.PolicyDocument, "logs:PutLogEvents") {
		t.Errorf("AWS-managed policy = %+v", policy)
	}

	if err := store.AttachRolePolicy(ctx, "fn", lambdaBasicExecutionARN); err != nil {
		t.Fatalf("AttachRolePolicy(AWS-managed): %v", err)
	}

	attached, err := store.ListAttachedRolePolicies(ctx, "fn")
	if err != nil || len(attached) != 1 || attached[0].PolicyName != "AWSLambdaBasicExecutionRole" {
		t.Fatalf("ListAttachedRolePolicies = %+v, %v", attached, err)
	}

	if got, _ := store.GetPolicy(ctx, lambdaBasicExecutionARN); got.AttachmentCount != 1 {
		t.Errorf("AttachmentCount = %d, want 1", got.AttachmentCount)
	}

	if err := store.DetachRolePolicy(ctx, "fn", lambdaBasicExecutionARN); err != nil {
		t.Fatalf("DetachRolePolicy: %v", err)
	}

	for _, arn := range []string{"arn:aws:iam::aws:policy/", "arn:aws:iam::aws:policy/service-role/", "arn:aws:iam::000000000000:policy/missing"} {
		if err := store.AttachRolePolicy(ctx, "fn", arn); err == nil {
			t.Errorf("AttachRolePolicy(%s) must fail", arn)
		}
	}
}
