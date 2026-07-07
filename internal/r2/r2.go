// Package r2 constructs an S3 client for Cloudflare R2's S3-compatible API. It
// is the single source of the R2-specific client wiring both the release store
// (ADR-0016) and the backups store (ADR-0036) share: the account-derived
// endpoint, path-style addressing, and the checksum settings R2 requires. It is
// deliberately store-agnostic — it knows nothing about key schemes or buckets,
// only how to talk to R2 — so the two stores stay separate (distinct buckets,
// distinct key schemes, distinct lifecycles) while reusing the one piece of
// fiddly, easy-to-get-wrong client construction.
package r2

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewClient builds an S3 client against Cloudflare R2 for the account named by
// accountID, using the standard AWS credential chain (AWS_ACCESS_KEY_ID /
// AWS_SECRET_ACCESS_KEY) for auth. region=auto is R2's pseudo-region;
// path-style addressing matches the state-backend translation. R2's S3 API does
// not fully accept the aws-sdk-go-v2 default of adding CRC checksums on every
// request (it surfaces as a 400 on the first write), so checksums are only
// sent/validated when an operation explicitly requires them.
func NewClient(ctx context.Context, accountID string) (*s3.Client, error) {
	if accountID == "" {
		return nil, errors.New("r2: empty Cloudflare account ID (set CLOUDFLARE_ACCOUNT_ID)")
	}
	// The account id is interpolated directly into the endpoint host, so validate it
	// is a lowercase hex string (as Cloudflare account ids are) rather than letting a
	// stray char produce a malformed endpoint that fails later with an opaque DNS/AWS
	// error. Mirrors the state/artifacts backend's r2Endpoint check so every R2 path
	// rejects a bad CLOUDFLARE_ACCOUNT_ID identically.
	for _, ch := range accountID {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return nil, fmt.Errorf("r2: CLOUDFLARE_ACCOUNT_ID must be a lowercase hex string, got %q", accountID)
		}
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("auto"))
	if err != nil {
		return nil, fmt.Errorf("r2: load AWS config: %w", err)
	}
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	}), nil
}
