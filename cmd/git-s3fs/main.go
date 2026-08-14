// Command git-s3fs stores large files in an S3 bucket you control, and keeps
// only a small pointer in the git repository.
//
// Installed on PATH as git-s3fs, it is reachable as `git s3fs <command>`.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/SeriousBug/gits3fs/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Main(ctx, os.Args[1:]))
}
