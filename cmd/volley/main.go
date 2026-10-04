package main

import (
	"context"
	"github.com/TheEditor/volley/internal/cli"
	"os"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr, cli.Options{}))
}
