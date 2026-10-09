package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/garyburd/vaultsite/site"
)

// lookupRegion is the endpoint region for bucket-location and CloudFront queries.
const lookupRegion = "us-east-1"

// Connect returns AWS implementations of Bucket and CDN using the standard
// credential chain. It discovers an unspecified bucket region or distribution
// from cfg. The CDN is nil if withCDN is false or no distribution matches.
// Callers must configure bucket access; Connect sets no ACLs or policies.
func Connect(ctx context.Context, cfg *site.Config, withCDN bool) (Bucket, CDN, error) {
	awscfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("loading AWS configuration: %w", err)
	}

	region := cfg.S3.Region
	if region == "" {
		c := s3.NewFromConfig(awscfg, func(o *s3.Options) { o.Region = lookupRegion })
		out, err := c.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: aws.String(cfg.S3.Bucket)})
		if err != nil {
			return nil, nil, fmt.Errorf("finding the region of bucket %s: %w", cfg.S3.Bucket, err)
		}
		// Normalize legacy region names.
		switch region = string(out.LocationConstraint); region {
		case "":
			region = "us-east-1"
		case "EU":
			region = "eu-west-1"
		}
	}
	bucket := &s3Bucket{
		client: s3.NewFromConfig(awscfg, func(o *s3.Options) { o.Region = region }),
		name:   cfg.S3.Bucket,
	}
	if !withCDN {
		return bucket, nil, nil
	}

	cf := cloudfront.NewFromConfig(awscfg, func(o *cloudfront.Options) { o.Region = lookupRegion })
	id := cfg.S3.DistributionID
	if id == "" {
		u, err := url.Parse(cfg.BaseURL)
		if err != nil {
			return nil, nil, err
		}
		if id, err = findDistribution(ctx, cf, u.Hostname()); err != nil {
			return nil, nil, fmt.Errorf("finding the CloudFront distribution for %s: %w", u.Hostname(), err)
		}
	}
	if id == "" {
		// Return a nil interface, not a typed nil.
		return bucket, nil, nil
	}
	return bucket, &cloudFront{client: cf, id: id}, nil
}

// findDistribution returns the ID of the distribution that has host among
// its aliases, or "".
func findDistribution(ctx context.Context, cf *cloudfront.Client, host string) (string, error) {
	p := cloudfront.NewListDistributionsPaginator(cf, &cloudfront.ListDistributionsInput{})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return "", err
		}
		if page.DistributionList == nil {
			continue
		}
		for _, d := range page.DistributionList.Items {
			if d.Aliases == nil {
				continue
			}
			for _, a := range d.Aliases.Items {
				if strings.EqualFold(a, host) {
					return aws.ToString(d.Id), nil
				}
			}
		}
	}
	return "", nil
}

type s3Bucket struct {
	client *s3.Client
	name   string
}

func (b *s3Bucket) List(ctx context.Context) ([]Object, error) {
	var out []Object
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{Bucket: aws.String(b.name)})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			out = append(out, Object{
				Key:     aws.ToString(o.Key),
				Size:    aws.ToInt64(o.Size),
				ETag:    aws.ToString(o.ETag),
				ModTime: aws.ToTime(o.LastModified),
			})
		}
	}
	return out, nil
}

// Use single-part uploads so unencrypted objects retain MD5 ETags.
func (b *s3Bucket) Put(ctx context.Context, key string, body io.Reader, meta Meta) error {
	// Signing requires a rewindable body.
	rs, ok := body.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		rs = bytes.NewReader(data)
	}
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(b.name),
		Key:          aws.String(key),
		Body:         rs,
		ContentType:  aws.String(meta.ContentType),
		CacheControl: aws.String(meta.CacheControl),
	})
	return err
}

// deleteBatch is the most keys one DeleteObjects request may name.
const deleteBatch = 1000

func (b *s3Bucket) Delete(ctx context.Context, keys []string) ([]string, error) {
	var deleted []string
	var errs []error
	for len(keys) > 0 {
		n := min(len(keys), deleteBatch)
		ids := make([]s3types.ObjectIdentifier, n)
		for i, k := range keys[:n] {
			ids[i] = s3types.ObjectIdentifier{Key: aws.String(k)}
		}
		keys = keys[n:]
		out, err := b.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(b.name),
			Delete: &s3types.Delete{Objects: ids},
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, d := range out.Deleted {
			deleted = append(deleted, aws.ToString(d.Key))
		}
		for _, e := range out.Errors {
			errs = append(errs, fmt.Errorf("%s: %s", aws.ToString(e.Key), aws.ToString(e.Message)))
		}
	}
	return deleted, errors.Join(errs...)
}

type cloudFront struct {
	client *cloudfront.Client
	id     string
}

func (c *cloudFront) InvalidateAll(ctx context.Context) (string, error) {
	out, err := c.client.CreateInvalidation(ctx, &cloudfront.CreateInvalidationInput{
		DistributionId: aws.String(c.id),
		InvalidationBatch: &cftypes.InvalidationBatch{
			// Reuse the reference on retries; each deployment gets a new one.
			CallerReference: aws.String("vaultsite-" + strconv.FormatInt(time.Now().UnixNano(), 10)),
			Paths:           &cftypes.Paths{Quantity: aws.Int32(1), Items: []string{"/*"}},
		},
	})
	if err != nil {
		return "", err
	}
	if out.Invalidation == nil {
		return "", nil
	}
	return aws.ToString(out.Invalidation.Id), nil
}
