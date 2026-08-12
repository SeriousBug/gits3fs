# git-s3fs pointer format, version 1

A git-s3fs pointer is the small text file that git stores in place of a large
object. It is UTF-8, line oriented, and terminated by a final newline.

```
version https://github.com/SeriousBug/gits3fs/spec/v1
url https://my-assets.s3.eu-west-1.amazonaws.com/site/objects/9f/86/9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
oid sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08
size 4823104
```

## Grammar

Each line is `key SP value LF`. Keys are case sensitive.

| Key | Required | Meaning |
| --- | --- | --- |
| `version` | yes | Always the spec URL above, and always the first line |
| `url` | no | Absolute URL of the object |
| `oid` | yes | `sha256:` followed by 64 lowercase hex characters |
| `size` | yes | Object size in bytes, decimal, non-negative |

A parser must ignore unknown keys, so the format can grow. A writer must emit
the keys in the order shown. A pointer file is at most 4096 bytes; anything
larger is treated as ordinary content.

`url` is optional because a repository may be configured without a bucket, but
writers should always emit it when they can. It is the difference between a
reader seeing a link they can follow and a reader seeing an opaque digest.

## Object layout

Objects are content addressed and stored at:

```
<prefix>/objects/<oid[0:2]>/<oid[2:4]>/<oid>
```

The two levels of fan-out keep bucket listings navigable. `<prefix>` may be
empty. Objects are immutable: the same content always has the same key, so a
CDN in front of the bucket may cache them indefinitely.

Uploads set `Cache-Control: public, max-age=31536000, immutable` and a
`gits3fs-sha256` metadata entry recording the digest.

## Determinism

The `url` of a pointer must depend only on repository-level configuration —
the committed `.gits3fs` file — and never on per-user git config or
environment variables.

git runs the clean filter every time a file is staged. If the URL varied
between machines, two people staging byte-identical content would produce
different pointers, and git would report modifications that do not exist.

A tool that finds its local configuration disagreeing with `.gits3fs` should
still write the shared URL, and warn.

## Resolving an object

Given a pointer, a client should try, in order:

1. The bucket in the local configuration, if one is configured.
2. The bucket implied by `url`, when the URL is recognisably an S3 endpoint.
   The bucket, region and key can be recovered from AWS, Cloudflare R2,
   DigitalOcean Spaces and Backblaze B2 URL forms; the configured prefix is
   whatever precedes `objects/<aa>/<bb>/<oid>` in the key.
3. A plain unauthenticated HTTPS `GET` of `url`. This covers CDNs and custom
   domains, which cannot be mapped back to a bucket but serve the bytes all
   the same.

Downloaded content must be hashed and compared against `oid` before it is
cached or written to the working tree. A mismatch is an error, not a warning.

## Relationship to git-lfs

The layout is deliberately similar to git-lfs, and git-lfs pointers
(`version https://git-lfs.github.com/spec/v1`) are recognisable so tools can
tell the two apart. They are not interchangeable: a git-lfs pointer has no
`url`, and git-lfs will not understand a git-s3fs pointer.
