package config

import "testing"

const oid = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestObjectKey(t *testing.T) {
	c := &Config{}
	want := "objects/9f/86/" + oid
	if got := c.ObjectKey(oid); got != want {
		t.Errorf("ObjectKey = %q, want %q", got, want)
	}
	c.Prefix = "repos/site"
	if got, want := c.ObjectKey(oid), "repos/site/objects/9f/86/"+oid; got != want {
		t.Errorf("ObjectKey with prefix = %q, want %q", got, want)
	}
}

func TestObjectURL(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "aws regional",
			cfg:  Config{Bucket: "my-assets", Region: "eu-west-1"},
			want: "https://my-assets.s3.eu-west-1.amazonaws.com/objects/9f/86/" + oid,
		},
		{
			name: "aws us-east-1 uses the legacy host",
			cfg:  Config{Bucket: "my-assets", Region: "us-east-1"},
			want: "https://my-assets.s3.amazonaws.com/objects/9f/86/" + oid,
		},
		{
			name: "with prefix",
			cfg:  Config{Bucket: "b", Region: "us-west-2", Prefix: "site"},
			want: "https://b.s3.us-west-2.amazonaws.com/site/objects/9f/86/" + oid,
		},
		{
			name: "custom endpoint, path style",
			cfg:  Config{Bucket: "b", Endpoint: "https://s3.example.com", PathStyle: true},
			want: "https://s3.example.com/b/objects/9f/86/" + oid,
		},
		{
			name: "custom endpoint, virtual host",
			cfg:  Config{Bucket: "b", Endpoint: "https://s3.example.com"},
			want: "https://b.s3.example.com/objects/9f/86/" + oid,
		},
		{
			name: "endpoint without a scheme",
			cfg:  Config{Bucket: "b", Endpoint: "s3.example.com", PathStyle: true},
			want: "https://s3.example.com/b/objects/9f/86/" + oid,
		},
		{
			name: "public URL wins",
			cfg:  Config{Bucket: "b", Region: "us-east-1", PublicURL: "https://cdn.example.com"},
			want: "https://cdn.example.com/objects/9f/86/" + oid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.cfg.ObjectURL(oid)
			if err != nil {
				t.Fatalf("ObjectURL: %v", err)
			}
			if got != tc.want {
				t.Errorf("ObjectURL =\n %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestObjectURLNeedsABucket(t *testing.T) {
	c := &Config{Region: "us-east-1"}
	if _, err := c.ObjectURL(oid); err == nil {
		t.Error("ObjectURL should fail without a bucket")
	}
}

func TestParseObjectURL(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		bucket    string
		region    string
		endpoint  string
		pathStyle bool
	}{
		{
			name: "virtual host with region", url: "https://b.s3.eu-west-1.amazonaws.com/objects/9f/86/" + oid,
			bucket: "b", region: "eu-west-1",
		},
		{
			name: "virtual host without region", url: "https://b.s3.amazonaws.com/objects/9f/86/" + oid,
			bucket: "b", region: "us-east-1",
		},
		{
			name: "path style", url: "https://s3.us-west-2.amazonaws.com/b/objects/9f/86/" + oid,
			bucket: "b", region: "us-west-2", pathStyle: true,
		},
		{
			name: "legacy dashed region", url: "https://b.s3-ap-southeast-2.amazonaws.com/objects/9f/86/" + oid,
			bucket: "b", region: "ap-southeast-2",
		},
		{
			name: "cloudflare r2", url: "https://acct123.r2.cloudflarestorage.com/b/objects/9f/86/" + oid,
			bucket: "b", region: "auto", endpoint: "https://acct123.r2.cloudflarestorage.com", pathStyle: true,
		},
		{
			name: "digitalocean spaces", url: "https://b.nyc3.digitaloceanspaces.com/objects/9f/86/" + oid,
			bucket: "b", region: "nyc3", endpoint: "https://nyc3.digitaloceanspaces.com",
		},
		{
			name: "backblaze b2", url: "https://s3.us-west-004.backblazeb2.com/b/objects/9f/86/" + oid,
			bucket: "b", region: "us-west-004", endpoint: "https://s3.us-west-004.backblazeb2.com", pathStyle: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, ok := ParseObjectURL(tc.url)
			if !ok {
				t.Fatalf("ParseObjectURL(%q) failed", tc.url)
			}
			if loc.Bucket != tc.bucket || loc.Region != tc.region ||
				loc.Endpoint != tc.endpoint || loc.PathStyle != tc.pathStyle {
				t.Errorf("got %+v", loc)
			}
			if prefix, ok := PrefixFromKey(loc.Key, oid); !ok || prefix != "" {
				t.Errorf("PrefixFromKey(%q) = %q, %v", loc.Key, prefix, ok)
			}
		})
	}
}

func TestParseObjectURLUnrecognised(t *testing.T) {
	// A CDN in front of the bucket cannot be mapped back to a bucket, and the
	// caller is expected to fall back to a plain HTTPS GET.
	for _, u := range []string{
		"https://cdn.example.com/objects/9f/86/" + oid,
		"not a url",
		"https://b.s3.amazonaws.com/",
	} {
		if loc, ok := ParseObjectURL(u); ok {
			t.Errorf("ParseObjectURL(%q) unexpectedly succeeded: %+v", u, loc)
		}
	}
}

func TestObjectURLParsesBackToItself(t *testing.T) {
	cfgs := []Config{
		{Bucket: "assets", Region: "eu-west-1", Prefix: "site"},
		{Bucket: "assets", Region: "us-east-1"},
		{Bucket: "assets", Region: "auto", Endpoint: "https://acct.r2.cloudflarestorage.com", PathStyle: true, Prefix: "deep/nested"},
	}
	for _, c := range cfgs {
		url, err := c.ObjectURL(oid)
		if err != nil {
			t.Fatal(err)
		}
		loc, ok := ParseObjectURL(url)
		if !ok {
			t.Fatalf("could not parse back %q", url)
		}
		if loc.Bucket != c.Bucket {
			t.Errorf("bucket round trip: got %q, want %q", loc.Bucket, c.Bucket)
		}
		prefix, ok := PrefixFromKey(loc.Key, oid)
		if !ok || prefix != c.Prefix {
			t.Errorf("prefix round trip: got %q (%v), want %q", prefix, ok, c.Prefix)
		}
	}
}

func TestPrefixFromKeyRejectsForeignLayout(t *testing.T) {
	if _, ok := PrefixFromKey("some/other/layout/"+oid, oid); ok {
		t.Error("PrefixFromKey should reject keys that are not in our layout")
	}
}
