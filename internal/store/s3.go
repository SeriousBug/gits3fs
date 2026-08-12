package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/SeriousBug/gits3fs/internal/config"
)

// S3 is the S3 backed object store.
type S3 struct {
	client   *s3.Client
	uploader *manager.Uploader
	cfg      *config.Config
	anon     bool
}

// NewS3 builds a store from the resolved repository configuration.
//
// Credentials come from the standard AWS chain: environment variables, the
// shared credentials file, SSO, container and instance roles. git-s3fs never
// reads or stores a secret of its own.
func NewS3(ctx context.Context, cfg *config.Config) (*S3, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	client, anon, err := newClient(ctx, cfg.Region, cfg.Endpoint, cfg.PathStyle, cfg.Profile, cfg.Anonymous)
	if err != nil {
		return nil, err
	}
	st := &S3{client: client, cfg: cfg, anon: anon}
	st.uploader = manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = cfg.ChunkSize
		// Objects are already uploaded in parallel with each other, so keep
		// per object part concurrency modest.
		u.Concurrency = 4
	})
	return st, nil
}

func newClient(ctx context.Context, region, endpoint string, pathStyle bool, profile string, anonymous bool) (*s3.Client, bool, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if region == "" {
		region = "us-east-1"
	}
	opts = append(opts, awsconfig.WithRegion(region))
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	anon := anonymous
	if anonymous {
		opts = append(opts, awsconfig.WithCredentialsProvider(aws.AnonymousCredentials{}))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, false, fmt.Errorf("loading AWS configuration: %w", err)
	}
	if !anonymous {
		// Fall back to unauthenticated access rather than failing outright:
		// a public asset bucket should be usable by someone who has never
		// configured AWS credentials.
		if _, err := awsCfg.Credentials.Retrieve(ctx); err != nil {
			awsCfg.Credentials = aws.AnonymousCredentials{}
			anon = true
		}
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = pathStyle
			// Several S3 compatible services reject the trailing checksums
			// the SDK adds by default, so only send them where required.
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		} else if pathStyle {
			o.UsePathStyle = true
		}
	})
	return client, anon, nil
}

// Anonymous reports whether the store resolved to unauthenticated access.
func (s *S3) Anonymous() bool { return s.anon }

// ReadOnly reports whether uploads are possible.
func (s *S3) ReadOnly() bool { return s.anon }

// Describe returns a human readable description of the destination.
func (s *S3) Describe() string {
	var b strings.Builder
	b.WriteString("s3://" + s.cfg.Bucket)
	if s.cfg.Prefix != "" {
		b.WriteString("/" + s.cfg.Prefix)
	}
	if s.cfg.Endpoint != "" {
		b.WriteString(" (" + s.cfg.Endpoint + ")")
	} else if s.cfg.Region != "" {
		b.WriteString(" (" + s.cfg.Region + ")")
	}
	if s.anon {
		b.WriteString(" [anonymous]")
	}
	return b.String()
}

// URL returns the public URL of an object.
func (s *S3) URL(oid string) (string, error) { return s.cfg.ObjectURL(oid) }

// Has reports whether an object is already stored.
func (s *S3) Has(ctx context.Context, oid string) (bool, int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.cfg.ObjectKey(oid)),
	})
	if err != nil {
		if isNotFound(err) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("checking %s: %w", oid, err)
	}
	return true, aws.ToInt64(out.ContentLength), nil
}

// Get streams an object into w.
func (s *S3) Get(ctx context.Context, oid string, w io.Writer) error {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.cfg.ObjectKey(oid)),
	})
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, oid)
		}
		return fmt.Errorf("downloading %s: %w", oid, err)
	}
	defer out.Body.Close()
	if _, err := io.Copy(w, out.Body); err != nil {
		return fmt.Errorf("downloading %s: %w", oid, err)
	}
	return nil
}

// Put uploads an object, using multipart uploads for large content.
func (s *S3) Put(ctx context.Context, oid string, r io.Reader, size int64) error {
	if s.anon {
		return errors.New("cannot upload: no AWS credentials were found")
	}
	in := &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.cfg.ObjectKey(oid)),
		Body:   r,
		// Objects are immutable and content addressed, so they can be cached
		// forever by any CDN sitting in front of the bucket.
		CacheControl: aws.String("public, max-age=31536000, immutable"),
		Metadata:     map[string]string{"gits3fs-sha256": oid},
	}
	if s.cfg.StorageClass != "" {
		in.StorageClass = types.StorageClass(s.cfg.StorageClass)
	}
	if s.cfg.SSE != "" {
		in.ServerSideEncryption = types.ServerSideEncryption(s.cfg.SSE)
		if s.cfg.SSEKMSKeyID != "" {
			in.SSEKMSKeyId = aws.String(s.cfg.SSEKMSKeyID)
		}
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if _, err := s.uploader.Upload(ctx, in); err != nil {
		return fmt.Errorf("uploading %s: %w", oid, err)
	}
	return nil
}

// CheckBucket verifies that the bucket exists and is reachable.
func (s *S3) CheckBucket(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.cfg.Bucket)})
	return err
}

func isNotFound(err error) bool {
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "404":
			return true
		}
	}
	return false
}
