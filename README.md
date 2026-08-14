# git-s3fs

Large files in git, stored in **your own S3 bucket**.

git-s3fs works the way git-lfs does — a clean/smudge filter swaps big files
for small pointers at commit time and swaps them back at checkout — but the
storage is an ordinary S3 bucket you control, not a service bolted onto your
git host. No LFS server, no per-repository quota, no vendor to migrate off.

**What lands in your repository is a link, not a hash.** A git-lfs pointer
gives a reader a bare digest and nothing to do with it. A git-s3fs pointer
records the object's actual URL, so anyone browsing the repo on GitHub can
copy the link and fetch the file — and a clone with no configuration at all
still knows where the bytes live.

```
version https://github.com/SeriousBug/gits3fs/spec/v1
url https://my-assets.s3.eu-west-1.amazonaws.com/site/objects/9f/86/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
oid sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
size 4823104
```

## Install

Pre-built binaries are on the [releases page](https://github.com/SeriousBug/gits3fs/releases).
Put `git-s3fs` anywhere on your `PATH` and git will expose it as `git s3fs`.

From source:

```sh
go install github.com/SeriousBug/gits3fs/cmd/git-s3fs@latest
```

Builds are pure Go with `CGO_ENABLED=0`, so a binary is a single static file
with no runtime dependencies.

## Getting started

```sh
git s3fs init --bucket my-assets --region eu-west-1 --prefix site
git s3fs track '*.psd' '*.mp4'
git add .gitattributes .gits3fs
git commit -m "Track design assets with git-s3fs"

git add design/hero.psd
git commit -m "Add hero artwork"
git push            # the pre-push hook uploads the object first
```

Everyone else just clones:

```sh
git clone git@github.com:you/your-repo.git
git s3fs install    # once per machine: git s3fs install --global
```

`.gits3fs` is committed, so a clone already knows the bucket. Credentials are
not: they come from the ordinary AWS chain.

## How it works

| Step | What happens |
| --- | --- |
| `git add big.psd` | The **clean filter** hashes the file, stores it in `.git/s3fs/objects/`, and hands git a pointer |
| `git commit` | git commits the pointer, a few hundred bytes |
| `git push` | The **pre-push hook** uploads any object the bucket does not have yet |
| `git checkout` | The **smudge filter** restores real content, downloading it if needed |

Objects are content addressed by SHA-256 and stored at
`<prefix>/objects/<aa>/<bb>/<sha256>`. They are immutable, so identical files
across branches and history are stored once, and a CDN in front of the bucket
can cache them forever.

## Configuration

Settings live in `.gitattributes` (which files) and `.gits3fs` (which bucket).
Both are meant to be committed.

`.gits3fs` uses git's own config syntax, so `git config -f .gits3fs` works on
it:

```ini
[s3fs]
	bucket = my-assets
	region = eu-west-1
	prefix = site
```

Every setting can be overridden per user through `git config s3fs.*`, and per
invocation through `GITS3FS_*` environment variables. Precedence is
environment, then git config, then `.gits3fs`, then defaults. `git s3fs env`
prints the resolved values and where each came from.

| Setting | git config / `.gits3fs` | Environment | Meaning |
| --- | --- | --- | --- |
| Bucket | `s3fs.bucket` | `GITS3FS_BUCKET` | Bucket holding the objects |
| Region | `s3fs.region` | `GITS3FS_REGION` | S3 region |
| Prefix | `s3fs.prefix` | `GITS3FS_PREFIX` | Key prefix inside the bucket |
| Endpoint | `s3fs.endpoint` | `GITS3FS_ENDPOINT` | S3-compatible endpoint |
| Path style | `s3fs.pathStyle` | `GITS3FS_PATH_STYLE` | `endpoint/bucket/key` addressing |
| Public URL | `s3fs.publicUrl` | `GITS3FS_PUBLIC_URL` | Base URL written into pointers (a CDN, say) |
| Anonymous | `s3fs.anonymous` | `GITS3FS_ANONYMOUS` | Read the bucket without credentials |
| Profile | `s3fs.profile` | `GITS3FS_PROFILE` | Named AWS profile |
| Storage class | `s3fs.storageClass` | `GITS3FS_STORAGE_CLASS` | e.g. `STANDARD_IA` |
| Encryption | `s3fs.sse`, `s3fs.sseKmsKeyId` | `GITS3FS_SSE`, `GITS3FS_SSE_KMS_KEY_ID` | Server-side encryption |
| Concurrency | `s3fs.concurrency` | `GITS3FS_CONCURRENCY` | Parallel transfers (default 8) |
| Part size | `s3fs.chunkSize` | `GITS3FS_CHUNK_SIZE` | Multipart part size (default 16 MiB) |
| Lazy | `s3fs.lazy` | `GITS3FS_LAZY` | Leave pointers in place on checkout; fetch later |

### Pointer URLs are deterministic on purpose

The URL written into a pointer comes **only** from the committed `.gits3fs`.
If it came from your personal git config, two people staging the same file
would produce different pointers and git would report changes that are not
there. Override the bucket locally if you like — your uploads follow the
override, and `git s3fs doctor` will warn you that the two disagree.

### Credentials

git-s3fs never stores a secret and never reads one from the repository.
Credentials are resolved by the AWS SDK's standard chain: environment
variables, `~/.aws/credentials`, SSO, container and instance roles.

If no credentials are found, git-s3fs falls back to unauthenticated access, so
a public asset bucket works for anyone who clones. Uploads, naturally, still
need credentials.

### Non-AWS S3

Anything with an S3 API works. Set `--endpoint`, and `--path-style` if the
service needs it:

```sh
# MinIO / Ceph / self-hosted
git s3fs init --bucket assets --endpoint https://s3.example.com --path-style --region us-east-1

# Cloudflare R2, with a public CDN domain in the pointers
git s3fs init --bucket assets --region auto \
    --endpoint https://<account>.r2.cloudflarestorage.com \
    --public-url https://cdn.example.com

# Backblaze B2
git s3fs init --bucket assets --endpoint https://s3.us-west-004.backblazeb2.com --region us-west-004
```

git-s3fs recognises AWS, R2, DigitalOcean Spaces and Backblaze URLs well
enough to fetch from a pointer alone. For any other URL — a CDN or custom
domain — it falls back to a plain HTTPS `GET`, which is exactly what such a
URL supports.

## Commands

| Command | What it does |
| --- | --- |
| `git s3fs init` | Set up filters, hooks and bucket settings |
| `git s3fs install` | Install just the filters and hooks (`--global` for all repos) |
| `git s3fs track '<pattern>'` | Route a pattern through git-s3fs; no arguments lists patterns |
| `git s3fs untrack '<pattern>'` | Stop routing a pattern |
| `git s3fs status` | Tracked files, pointers still to download, objects still to upload |
| `git s3fs ls-files` | List managed files, with `--url`, `--size`, `--oid` |
| `git s3fs push` | Upload objects (the pre-push hook does this for you) |
| `git s3fs fetch` | Download objects into the cache |
| `git s3fs checkout` | Turn pointers in the working tree into real files |
| `git s3fs pull` | `fetch` then `checkout` |
| `git s3fs url <path>` | Print the object URL for a file |
| `git s3fs prune` | Drop cached objects nothing refers to any more |
| `git s3fs migrate import\|export\|info` | Convert files to or from pointers |
| `git s3fs doctor` | Check the setup and say what is wrong |
| `git s3fs env` | Show resolved configuration and where it came from |

Run `git s3fs help <command>` for details.

## Converting an existing repository

```sh
git s3fs migrate info --above 5mb                 # what is big?
git s3fs migrate import --include '*.psd'         # convert and re-stage
git commit -m "Move design assets to git-s3fs"
git push
```

This rewrites the index and working tree, not history: old commits keep their
old blobs, so checking one out gives you the original file. To rewrite history
too, use [git-filter-repo](https://github.com/newren/git-filter-repo) and run
`git s3fs migrate import` as its conversion step.

Coming from git-lfs? git-s3fs recognises git-lfs pointers and leaves them
alone. Run `git lfs pull` so the real contents are in your working tree, drop
the `filter=lfs` lines from `.gitattributes`, then `git s3fs migrate import`.

## Bucket policy

git-s3fs needs `s3:GetObject`, `s3:PutObject` and `s3:ListBucket` on the
prefix it uses. It never deletes anything from the bucket — `prune` only
touches the local cache — so you can safely deny `s3:DeleteObject`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject"],
      "Resource": "arn:aws:s3:::my-assets/site/*"
    },
    {
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::my-assets"
    }
  ]
}
```

Because objects are immutable and content addressed, versioning is optional
and lifecycle rules are safe as long as you do not expire objects that history
still refers to.

## Development

```sh
make build     # build ./bin/git-s3fs
make test      # unit and end-to-end tests
make check     # vet, gofmt and tests
```

The end-to-end tests drive the real binary through real git against an
in-process fake S3, covering the filters, the pre-push hook and a fresh clone.
They need `git` on `PATH` and no network.

## License

MIT. See [LICENSE](LICENSE).
