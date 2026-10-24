//go:build integration

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/acm/types"
	"github.com/aws/smithy-go"
	"github.com/sivchari/golden"
)

func newACMClient(t *testing.T) *acm.Client {
	t.Helper()

	return acm.NewFromConfig(awsConfig(t), func(o *acm.Options) {
		o.BaseEndpoint = aws.String(testEndpoint())
	})
}

// requireACMError fails unless err is the ACM exception E sent with HTTP 400:
// the ACM API reference lists the operation-specific exceptions checked here,
// including ResourceNotFoundException, with HTTP 400.
func requireACMError[E error](t *testing.T, err error) {
	t.Helper()

	var target E
	if !errors.As(err, &target) {
		t.Fatalf("expected %T, got %v", target, err)
	}

	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("no HTTP response in %v", err)
	}

	if got := respErr.HTTPStatusCode(); got != http.StatusBadRequest {
		t.Errorf("HTTP status: got %d, want 400", got)
	}
}

// acmError is the error code and HTTP status of a failed ACM call, recorded
// in goldens.
type acmError struct {
	Code       string
	HTTPStatus int
}

// acmErrorOf returns the code and HTTP status of err, which must be an ACM
// API error.
func acmErrorOf(t *testing.T, err error) *acmError {
	t.Helper()

	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected an API error, got %v", err)
	}

	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("no HTTP response in %v", err)
	}

	return &acmError{Code: apiErr.ErrorCode(), HTTPStatus: respErr.HTTPStatusCode()}
}

// describePublicKey names the algorithm and size of a certificate's public
// key, such as "RSA 2048" or "ECDSA P-256".
func describePublicKey(t *testing.T, key any) string {
	t.Helper()

	switch k := key.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d", k.N.BitLen())
	case *ecdsa.PublicKey:
		return "ECDSA " + k.Curve.Params().Name
	default:
		t.Fatalf("unexpected public key type %T", key)

		return ""
	}
}

func TestACM_RequestAndDescribeCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Request certificate.
	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName: aws.String("example.com"),
		SubjectAlternativeNames: []string{
			"www.example.com",
			"api.example.com",
		},
		ValidationMethod: types.ValidationMethodDns,
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("CertificateArn", "ResultMetadata")).Assert(t.Name()+"_request", requestOutput)

	arn := *requestOutput.CertificateArn

	// Describe certificate.
	describeOutput, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("CertificateArn", "CreatedAt", "IssuedAt", "NotBefore", "NotAfter", "Serial", "ResultMetadata")).Assert(t.Name()+"_describe", describeOutput)
}

func TestACM_ListCertificates(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Request a certificate first.
	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName: aws.String("list-test.example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}

	arn := *requestOutput.CertificateArn

	// List certificates.
	listOutput, err := client.ListCertificates(ctx, &acm.ListCertificatesInput{
		MaxItems: aws.Int32(10),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find our certificate.
	found := false

	for _, cert := range listOutput.CertificateSummaryList {
		if cert.CertificateArn != nil && *cert.CertificateArn == arn {
			found = true

			break
		}
	}

	if !found {
		t.Errorf("certificate %s not found in list", arn)
	}
}

func TestACM_ListCertificatesWithStatusFilter(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Request an email-validated certificate, which stays PENDING_VALIDATION.
	_, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("filter-test.example.com"),
		ValidationMethod: types.ValidationMethodEmail,
	})
	if err != nil {
		t.Fatal(err)
	}

	// List with PENDING_VALIDATION filter.
	listOutput, err := client.ListCertificates(ctx, &acm.ListCertificatesInput{
		CertificateStatuses: []types.CertificateStatus{
			types.CertificateStatusPendingValidation,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// All returned certificates should be PENDING_VALIDATION.
	for _, cert := range listOutput.CertificateSummaryList {
		if cert.Status != types.CertificateStatusPendingValidation {
			t.Errorf("unexpected status: got %s, want PENDING_VALIDATION", cert.Status)
		}
	}
}

func TestACM_DeleteCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Request a certificate.
	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName: aws.String("delete-test.example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}

	arn := *requestOutput.CertificateArn

	// Delete the certificate.
	_, err = client.DeleteCertificate(ctx, &acm.DeleteCertificateInput{
		CertificateArn: aws.String(arn),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify it's deleted by trying to describe it.
	_, err = client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	})
	if err == nil {
		t.Fatal("expected error when describing deleted certificate")
	}
}

func TestACM_ImportCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Import a certificate (using dummy data - the emulator doesn't validate).
	importOutput, err := client.ImportCertificate(ctx, &acm.ImportCertificateInput{
		Certificate: []byte("-----BEGIN CERTIFICATE-----\nMIICert...\n-----END CERTIFICATE-----"),
		PrivateKey:  []byte("-----BEGIN PRIVATE KEY-----\nMIIPrivateKey...\n-----END PRIVATE KEY-----"),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("CertificateArn", "ResultMetadata")).Assert(t.Name()+"_import", importOutput)

	arn := *importOutput.CertificateArn

	// Describe the imported certificate.
	describeOutput, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("CertificateArn", "CreatedAt", "IssuedAt", "ImportedAt", "Serial", "ResultMetadata")).Assert(t.Name()+"_describe", describeOutput)
}

func TestACM_RequestCertificateWithOptions(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Request certificate with key algorithm option.
	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:   aws.String("options-test.example.com"),
		KeyAlgorithm: types.KeyAlgorithmEcPrime256v1,
		Options: &types.CertificateOptions{
			CertificateTransparencyLoggingPreference: types.CertificateTransparencyLoggingPreferenceEnabled,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("CertificateArn", "ResultMetadata")).Assert(t.Name()+"_request", requestOutput)

	arn := *requestOutput.CertificateArn

	// Describe to verify options.
	describeOutput, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String(arn),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("CertificateArn", "CreatedAt", "IssuedAt", "NotBefore", "NotAfter", "Serial", "ResultMetadata")).Assert(t.Name()+"_describe", describeOutput)
}

// TestACM_RequestCertificateKeyAlgorithms requests a certificate with each
// KeyAlgorithm value, plus an empty and an undefined one, and records the
// algorithm DescribeCertificate reports and the key of the certificate
// GetCertificate returns, or the error.
func TestACM_RequestCertificateKeyAlgorithms(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	type outcome struct {
		KeyAlgorithm string
		Described    string    `json:",omitempty"`
		PublicKey    string    `json:",omitempty"`
		Error        *acmError `json:",omitempty"`
	}

	algorithms := []types.KeyAlgorithm{
		"", // the default
		types.KeyAlgorithmRsa2048,
		types.KeyAlgorithmEcPrime256v1,
		types.KeyAlgorithmEcSecp384r1,
		types.KeyAlgorithmRsa1024,
		types.KeyAlgorithmRsa3072,
		types.KeyAlgorithmRsa4096,
		types.KeyAlgorithmEcSecp521r1,
		"RSA_9999", // not a KeyAlgorithm value
	}

	outcomes := make([]outcome, 0, len(algorithms))

	for _, algorithm := range algorithms {
		requested, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
			DomainName:   aws.String("key-algorithm.example.com"),
			KeyAlgorithm: algorithm,
		})
		if err != nil {
			outcomes = append(outcomes, outcome{KeyAlgorithm: string(algorithm), Error: acmErrorOf(t, err)})

			continue
		}

		t.Cleanup(func() {
			_, _ = client.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{
				CertificateArn: requested.CertificateArn,
			})
		})

		described, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: requested.CertificateArn})
		if err != nil {
			t.Fatal(err)
		}

		got, err := client.GetCertificate(ctx, &acm.GetCertificateInput{CertificateArn: requested.CertificateArn})
		if err != nil {
			t.Fatal(err)
		}

		block, _ := pem.Decode([]byte(aws.ToString(got.Certificate)))
		if block == nil {
			t.Fatalf("Certificate is not PEM: %q", aws.ToString(got.Certificate))
		}

		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse certificate: %v", err)
		}

		outcomes = append(outcomes, outcome{
			KeyAlgorithm: string(algorithm),
			Described:    string(described.Certificate.KeyAlgorithm),
			PublicKey:    describePublicKey(t, leaf.PublicKey),
		})
	}

	golden.New(t).Assert(t.Name(), outcomes)
}

func TestACM_DescribeNonExistentCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Try to describe a non-existent certificate.
	_, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{
		CertificateArn: aws.String("arn:aws:acm:us-east-1:000000000000:certificate/non-existent"),
	})
	requireACMError[*types.ResourceNotFoundException](t, err)
}

func TestACM_DeleteNonExistentCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Try to delete a non-existent certificate.
	_, err := client.DeleteCertificate(ctx, &acm.DeleteCertificateInput{
		CertificateArn: aws.String("arn:aws:acm:us-east-1:000000000000:certificate/non-existent"),
	})
	requireACMError[*types.ResourceNotFoundException](t, err)
}

func TestACM_GetCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Import a certificate first (imported certificates are in ISSUED state).
	importOutput, err := client.ImportCertificate(ctx, &acm.ImportCertificateInput{
		Certificate:      []byte("-----BEGIN CERTIFICATE-----\nMIICert...\n-----END CERTIFICATE-----"),
		PrivateKey:       []byte("-----BEGIN PRIVATE KEY-----\nMIIPrivateKey...\n-----END PRIVATE KEY-----"),
		CertificateChain: []byte("-----BEGIN CERTIFICATE-----\nMIIChain...\n-----END CERTIFICATE-----"),
	})
	if err != nil {
		t.Fatal(err)
	}

	arn := *importOutput.CertificateArn

	// Get the certificate.
	getOutput, err := client.GetCertificate(ctx, &acm.GetCertificateInput{
		CertificateArn: aws.String(arn),
	})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("ResultMetadata")).Assert(t.Name(), getOutput)
}

func TestACM_GetCertificatePendingValidation(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	// Request an email-validated certificate (will be in PENDING_VALIDATION state).
	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("pending.example.com"),
		ValidationMethod: types.ValidationMethodEmail,
	})
	if err != nil {
		t.Fatal(err)
	}

	arn := *requestOutput.CertificateArn

	// Try to get the certificate (should fail because it's not issued).
	_, err = client.GetCertificate(ctx, &acm.GetCertificateInput{
		CertificateArn: aws.String(arn),
	})

	requireACMError[*types.RequestInProgressException](t, err)
}

func TestACM_GetIssuedCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:              aws.String("get-issued.example.com"),
		SubjectAlternativeNames: []string{"*.get-issued.example.com"},
		ValidationMethod:        types.ValidationMethodDns,
	})
	if err != nil {
		t.Fatal(err)
	}

	arn := requestOutput.CertificateArn

	t.Cleanup(func() {
		_, _ = client.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{
			CertificateArn: arn,
		})
	})

	waiter := acm.NewCertificateValidatedWaiter(client)
	if err := waiter.Wait(ctx, &acm.DescribeCertificateInput{CertificateArn: arn}, 30*time.Second); err != nil {
		t.Fatalf("wait for certificate validation: %v", err)
	}

	getOutput, err := client.GetCertificate(ctx, &acm.GetCertificateInput{CertificateArn: arn})
	if err != nil {
		t.Fatal(err)
	}

	block, _ := pem.Decode([]byte(aws.ToString(getOutput.Certificate)))
	if block == nil {
		t.Fatalf("Certificate is not PEM: %q", aws.ToString(getOutput.Certificate))
	}

	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	if want := []string{"get-issued.example.com", "*.get-issued.example.com"}; !slices.Equal(leaf.DNSNames, want) {
		t.Errorf("DNSNames: got %v, want %v", leaf.DNSNames, want)
	}

	chainBlock, _ := pem.Decode([]byte(aws.ToString(getOutput.CertificateChain)))
	if chainBlock == nil {
		t.Fatalf("CertificateChain is not PEM: %q", aws.ToString(getOutput.CertificateChain))
	}

	ca, err := x509.ParseCertificate(chainBlock.Bytes)
	if err != nil {
		t.Fatalf("parse chain: %v", err)
	}

	if err := leaf.CheckSignatureFrom(ca); err != nil {
		t.Errorf("certificate is not signed by its chain: %v", err)
	}

	describeOutput, err := client.DescribeCertificate(ctx, &acm.DescribeCertificateInput{CertificateArn: arn})
	if err != nil {
		t.Fatal(err)
	}

	if notAfter := aws.ToTime(describeOutput.Certificate.NotAfter); !notAfter.Equal(leaf.NotAfter) {
		t.Errorf("NotAfter: describe %v, certificate %v", notAfter, leaf.NotAfter)
	}
}

func TestACM_DNSValidatedCertificateIsIssued(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName:       aws.String("issued.example.com"),
		ValidationMethod: types.ValidationMethodDns,
	})
	if err != nil {
		t.Fatal(err)
	}

	arn := requestOutput.CertificateArn

	t.Cleanup(func() {
		_, _ = client.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{
			CertificateArn: arn,
		})
	})

	// The SDK waiter polls DescribeCertificate until the certificate is ISSUED.
	waiter := acm.NewCertificateValidatedWaiter(client)
	if err := waiter.Wait(ctx, &acm.DescribeCertificateInput{CertificateArn: arn}, 30*time.Second); err != nil {
		t.Fatalf("wait for certificate validation: %v", err)
	}
}

func TestACM_CertificateTags(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	requestOutput, err := client.RequestCertificate(ctx, &acm.RequestCertificateInput{
		DomainName: aws.String("tags.example.com"),
		Tags: []types.Tag{
			{Key: aws.String("env"), Value: aws.String("dev")},
			{Key: aws.String("team"), Value: aws.String("web")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	arn := requestOutput.CertificateArn

	t.Cleanup(func() {
		_, _ = client.DeleteCertificate(context.Background(), &acm.DeleteCertificateInput{
			CertificateArn: arn,
		})
	})

	listOutput, err := client.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{CertificateArn: arn})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("ResultMetadata")).Assert(t.Name()+"_requested", listOutput)

	_, err = client.AddTagsToCertificate(ctx, &acm.AddTagsToCertificateInput{
		CertificateArn: arn,
		Tags: []types.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
			{Key: aws.String("owner"), Value: aws.String("alice")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.RemoveTagsFromCertificate(ctx, &acm.RemoveTagsFromCertificateInput{
		CertificateArn: arn,
		Tags:           []types.Tag{{Key: aws.String("team")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	listOutput, err = client.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{CertificateArn: arn})
	if err != nil {
		t.Fatal(err)
	}

	golden.New(t, golden.WithIgnoreFields("ResultMetadata")).Assert(t.Name()+"_updated", listOutput)
}

func TestACM_ListTagsForNonExistentCertificate(t *testing.T) {
	client := newACMClient(t)
	ctx := t.Context()

	_, err := client.ListTagsForCertificate(ctx, &acm.ListTagsForCertificateInput{
		CertificateArn: aws.String("arn:aws:acm:us-east-1:000000000000:certificate/non-existent"),
	})

	requireACMError[*types.ResourceNotFoundException](t, err)
}
