package config

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// ObjectKey returns the S3 key an object is stored under.
//
// Objects are content addressed and fanned out two levels, so that a bucket
// listing stays navigable even with hundreds of thousands of objects:
//
//	<prefix>/objects/9f/86/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
func (c *Config) ObjectKey(oid string) string {
	key := path.Join("objects", oid[0:2], oid[2:4], oid)
	if c.Prefix != "" {
		key = path.Join(c.Prefix, key)
	}
	return key
}

// ObjectURL returns the URL written into a pointer file for an object.
//
// This is the whole point of git-s3fs over git-lfs: what lands in the
// repository is a link a human can open or copy, not an opaque digest.
func (c *Config) ObjectURL(oid string) (string, error) {
	key := c.ObjectKey(oid)

	if c.PublicURL != "" {
		return c.PublicURL + "/" + key, nil
	}
	if c.Bucket == "" {
		return "", fmt.Errorf("cannot build an object URL: no bucket configured")
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil {
			return "", fmt.Errorf("invalid endpoint %q: %w", c.Endpoint, err)
		}
		if u.Scheme == "" {
			// Bare hosts are common in S3 tooling; assume TLS.
			u, err = url.Parse("https://" + c.Endpoint)
			if err != nil {
				return "", fmt.Errorf("invalid endpoint %q: %w", c.Endpoint, err)
			}
		}
		if c.PathStyle {
			u.Path = "/" + strings.TrimLeft(path.Join(u.Path, c.Bucket, key), "/")
		} else {
			u.Host = c.Bucket + "." + u.Host
			u.Path = "/" + strings.TrimLeft(path.Join(u.Path, key), "/")
		}
		return u.String(), nil
	}
	host := c.Bucket + ".s3.amazonaws.com"
	if c.Region != "" && c.Region != "us-east-1" {
		host = fmt.Sprintf("%s.s3.%s.amazonaws.com", c.Bucket, c.Region)
	}
	return "https://" + host + "/" + key, nil
}

// PrefixFromKey recovers the configured key prefix from a full object key,
// given the object id it encodes. It lets a clone with no configuration
// reconstruct the layout of a bucket purely from a pointer URL.
func PrefixFromKey(key, oid string) (string, bool) {
	if len(oid) < 4 {
		return "", false
	}
	suffix := path.Join("objects", oid[0:2], oid[2:4], oid)
	trimmed, ok := strings.CutSuffix(key, suffix)
	if !ok {
		return "", false
	}
	return strings.Trim(trimmed, "/"), true
}

// Location is a bucket and key recovered from a pointer URL. It lets a clone
// with no configuration at all fetch its objects, because the pointer itself
// says where they live.
type Location struct {
	Bucket    string
	Key       string
	Region    string
	Endpoint  string // empty means AWS
	PathStyle bool
}

// ParseObjectURL recovers the bucket and key from a pointer URL.
//
// It understands the addressing schemes of the major S3 providers. For
// anything else, such as a CDN or custom domain in front of a bucket, it
// returns false and the caller falls back to a plain unauthenticated HTTPS
// GET, which is all such a URL can support anyway.
func ParseObjectURL(raw string) (*Location, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	key := strings.TrimPrefix(u.Path, "/")
	if key == "" {
		return nil, false
	}

	splitBucket := func() (string, string, bool) {
		bucket, rest, ok := strings.Cut(key, "/")
		if !ok || bucket == "" || rest == "" {
			return "", "", false
		}
		return bucket, rest, true
	}

	switch {
	case strings.HasSuffix(host, ".amazonaws.com"):
		labels := strings.Split(strings.TrimSuffix(host, ".amazonaws.com"), ".")
		// Path style: s3.amazonaws.com or s3.<region>.amazonaws.com
		if labels[0] == "s3" || strings.HasPrefix(labels[0], "s3-") {
			bucket, rest, ok := splitBucket()
			if !ok {
				return nil, false
			}
			return &Location{Bucket: bucket, Key: rest, Region: awsRegion(labels), PathStyle: true}, true
		}
		// Virtual host style: <bucket>.s3[.<region>].amazonaws.com
		for i, l := range labels {
			if l == "s3" || strings.HasPrefix(l, "s3-") {
				bucket := strings.Join(labels[:i], ".")
				if bucket == "" {
					return nil, false
				}
				return &Location{Bucket: bucket, Key: key, Region: awsRegion(labels[i:])}, true
			}
		}
		return nil, false

	case strings.HasSuffix(host, ".r2.cloudflarestorage.com"):
		// <account>.r2.cloudflarestorage.com/<bucket>/<key>
		bucket, rest, ok := splitBucket()
		if !ok {
			return nil, false
		}
		return &Location{
			Bucket:    bucket,
			Key:       rest,
			Region:    "auto",
			Endpoint:  u.Scheme + "://" + u.Host,
			PathStyle: true,
		}, true

	case strings.HasSuffix(host, ".digitaloceanspaces.com"):
		// <bucket>.<region>.digitaloceanspaces.com/<key>
		labels := strings.Split(strings.TrimSuffix(host, ".digitaloceanspaces.com"), ".")
		if len(labels) < 2 {
			return nil, false
		}
		region := labels[len(labels)-1]
		bucket := strings.Join(labels[:len(labels)-1], ".")
		return &Location{
			Bucket:   bucket,
			Key:      key,
			Region:   region,
			Endpoint: fmt.Sprintf("%s://%s.digitaloceanspaces.com", u.Scheme, region),
		}, true

	case strings.HasSuffix(host, ".backblazeb2.com"):
		// s3.<region>.backblazeb2.com/<bucket>/<key>
		labels := strings.Split(host, ".")
		if labels[0] != "s3" || len(labels) < 4 {
			return nil, false
		}
		bucket, rest, ok := splitBucket()
		if !ok {
			return nil, false
		}
		return &Location{
			Bucket:    bucket,
			Key:       rest,
			Region:    labels[1],
			Endpoint:  u.Scheme + "://" + u.Host,
			PathStyle: true,
		}, true
	}
	return nil, false
}

// awsRegion extracts the region from the labels of an AWS S3 host, given the
// labels starting at the "s3" component.
func awsRegion(labels []string) string {
	if len(labels) == 0 {
		return "us-east-1"
	}
	if r, ok := strings.CutPrefix(labels[0], "s3-"); ok && r != "" {
		return r // legacy s3-<region> form
	}
	if len(labels) > 1 && labels[1] != "" {
		return labels[1]
	}
	return "us-east-1"
}

// ExampleObjectURL renders the object URL with placeholders in place of a
// digest, for messages that describe where objects will go.
func (c *Config) ExampleObjectURL() (string, error) {
	placeholder := strings.Repeat("0", 64)
	url, err := c.ObjectURL(placeholder)
	if err != nil {
		return "", err
	}
	return strings.Replace(url,
		"objects/00/00/"+placeholder,
		"objects/<aa>/<bb>/<sha256>", 1), nil
}
