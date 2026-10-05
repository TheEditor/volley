package main

import (
	"context"
	"github.com/TheEditor/volley/internal/cli"
	"os"
	"path/filepath"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.Options{Entrypoint: filepath.Base(os.Args[0])}))
}
