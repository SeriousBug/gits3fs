# git-s3fs

Large files in git, stored in **your own S3 bucket**.

git-s3fs works like git-lfs, only committing small pointer files instead of
large files. Git LFS requires a compatible git server, and locks you into
that provider. git-s3fs instead lets you use any S3-compatible storage
service.

The S3 server settings are checked into the repository so others can
download the files. For public repos, you can use a public bucket to allow
anonymous downloads too. Credentials are stored outside the repository, in
the standard AWS credentials format.

## Install

Linux, macOS, FreeBSD, or Windows via Git Bash/WSL:

```sh
curl -fsSL https://raw.githubusercontent.com/SeriousBug/gits3fs/main/install.sh | sh
```

Windows via PowerShell:

```powershell
irm https://raw.githubusercontent.com/SeriousBug/gits3fs/main/install.ps1 | iex
```

This downloads the right binary for your platform and puts it on your
`PATH`, so git can find it as `git s3fs`. It's a standalone binary; nothing
else to install.

You can also grab a binary yourself from the
[releases page](https://github.com/SeriousBug/gits3fs/releases) and put
`git-s3fs` anywhere on your `PATH`.

## Getting started

Set up a repository to store large files in your bucket:

```sh
git s3fs init --bucket my-assets --region eu-west-1 --prefix site
git s3fs track '*.psd' '*.mp4'
git add .gitattributes .gits3fs
git commit -m "Track design assets with git-s3fs"

git add design/hero.psd
git commit -m "Add hero artwork"
git push            # uploads the file to your bucket automatically
```

Everyone else just clones and installs git-s3fs:

```sh
git clone git@github.com:you/your-repo.git
git s3fs install    # add --global to do this once for every repo instead
```

The bucket configuration is stored in the repo, so the commands above are
sufficient for people to check out files from public buckets. To upload
files, or to check out private buckets, others will also need to set up
credentials.

Credentials go in `~/.aws/credentials`, the same file the AWS CLI uses:

```ini
[default]
aws_access_key_id = AKIAIOSFODNN7EXAMPLE
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
```

This works the same way for non-AWS services like Backblaze B2: use the
access key ID and application key B2 gives you, in the same file. If your
bucket uses a specific named profile rather than `[default]`, point
git-s3fs at it with `git config s3fs.profile <name>` or
`GITS3FS_PROFILE`.

## Configuration

Settings live in two files, both meant to be committed:

- `.gitattributes`: which files are tracked
- `.gits3fs`: which bucket to use

```ini
[s3fs]
	bucket = my-assets
	region = eu-west-1
	prefix = site
```

You can override any setting for yourself with `git config s3fs.<name>
<value>`, or per command with a `GITS3FS_<NAME>` environment variable.
`git s3fs env` shows you the current settings and where each one is coming
from.

| Setting | git config | Environment variable | Meaning |
| --- | --- | --- | --- |
| Bucket | `s3fs.bucket` | `GITS3FS_BUCKET` | Bucket holding the files |
| Region | `s3fs.region` | `GITS3FS_REGION` | S3 region |
| Prefix | `s3fs.prefix` | `GITS3FS_PREFIX` | Folder inside the bucket |
| Endpoint | `s3fs.endpoint` | `GITS3FS_ENDPOINT` | Non-AWS S3-compatible service |
| Path style | `s3fs.pathStyle` | `GITS3FS_PATH_STYLE` | Needed by some non-AWS services |
| Public URL | `s3fs.publicUrl` | `GITS3FS_PUBLIC_URL` | Use a CDN domain in download links instead of the bucket's own |
| Anonymous | `s3fs.anonymous` | `GITS3FS_ANONYMOUS` | Download without AWS credentials (for public buckets) |
| Profile | `s3fs.profile` | `GITS3FS_PROFILE` | Which named AWS profile to use |
| Storage class | `s3fs.storageClass` | `GITS3FS_STORAGE_CLASS` | e.g. `STANDARD_IA`, for cheaper storage |
| Encryption | `s3fs.sse`, `s3fs.sseKmsKeyId` | `GITS3FS_SSE`, `GITS3FS_SSE_KMS_KEY_ID` | Encrypt files at rest |
| Concurrency | `s3fs.concurrency` | `GITS3FS_CONCURRENCY` | How many files to transfer at once (default 8) |
| Part size | `s3fs.chunkSize` | `GITS3FS_CHUNK_SIZE` | Upload chunk size for large files (default 16 MiB) |
| Lazy | `s3fs.lazy` | `GITS3FS_LAZY` | Skip downloading files on checkout until you actually need them |

If you change any settings locally, run `git s3fs doctor` afterward to make
sure everything is still configured correctly.

### Credentials

git-s3fs picks up AWS credentials the same way the AWS CLI does: environment
variables, `~/.aws/credentials`, SSO, or your cloud provider's built-in
roles. You don't configure credentials in git-s3fs itself.

For public buckets, git-s3fs can download files without authentication to
allow anonymous checkouts for public repos. Uploading still requires
credentials.

### Using something other than AWS S3

Any storage service with an S3-compatible API works. Set `--endpoint`, and
`--path-style` if the service needs it:

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

git-s3fs can tell which bucket, region and endpoint a pointer's URL points to
for AWS, R2, DigitalOcean Spaces and Backblaze, so downloads work correctly
even from a machine with no `.gits3fs` at all. Any other URL, such as a CDN
or custom domain, is downloaded with a plain HTTPS request instead, which
works the same either way.

## Commands

| Command | What it does |
| --- | --- |
| `git s3fs init` | Set up a repository to use git-s3fs |
| `git s3fs install` | Set up git-s3fs on your machine for a repo that already uses it (`--global` for all repos) |
| `git s3fs track '<pattern>'` | Start storing files matching a pattern in the bucket; no arguments lists patterns |
| `git s3fs untrack '<pattern>'` | Stop storing a pattern in the bucket |
| `git s3fs status` | See tracked files, and what still needs uploading or downloading |
| `git s3fs ls-files` | List managed files, with `--url`, `--size`, `--oid` |
| `git s3fs push` | Upload files to the bucket (normally done automatically on `git push`) |
| `git s3fs fetch` | Download files from the bucket into your local cache |
| `git s3fs checkout` | Restore real files in your working tree from pointers |
| `git s3fs pull` | `fetch` then `checkout` |
| `git s3fs url <path>` | Print the download URL for a file |
| `git s3fs prune` | Free up local disk space by dropping cached files you no longer need |
| `git s3fs migrate import\|export\|info` | Convert files to or from git-s3fs |
| `git s3fs doctor` | Check your setup and report any problems |
| `git s3fs env` | Show your current settings and where each one comes from |

Run `git s3fs help <command>` for details.

## Converting an existing repository

### From files already checked into git

```sh
git s3fs init --bucket my-assets --region eu-west-1 --prefix site
git s3fs migrate info --above 5mb                 # what is big?
git s3fs migrate import --include '*.psd'         # convert and re-stage
git commit -m "Move design assets to git-s3fs"
git push
```

This only changes files going forward. Old commits still contain the
original files, so checking one out gives you the file as it was. If you
also want to remove those large files from your repository's history (to
shrink a `.git` folder, for example), use
[git-filter-repo](https://github.com/newren/git-filter-repo) and run
`git s3fs migrate import` as its conversion step.

### From git-lfs

git-s3fs recognizes git-lfs pointers and leaves them alone until you convert
them:

```sh
git s3fs init --bucket my-assets --region eu-west-1 --prefix site
git lfs pull                                      # make sure real files are checked out
```

Then remove the `filter=lfs` lines from `.gitattributes` and replace them
with `git s3fs track '<pattern>'` for the same patterns, and run:

```sh
git s3fs migrate import --include '*.psd'
git commit -m "Move design assets from git-lfs to git-s3fs"
git push
```

## Bucket permissions

git-s3fs needs `s3:GetObject`, `s3:PutObject` and `s3:ListBucket` on the
prefix it uses. It never deletes anything from your bucket (`prune` only
clears your local cache), so you can safely deny `s3:DeleteObject`:

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

Bucket versioning and lifecycle rules are safe to use, as long as your
lifecycle rules don't expire files that older commits still need.

## License

MIT. See [LICENSE](LICENSE).
