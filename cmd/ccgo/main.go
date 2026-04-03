package main

import (
	"context"
	"os"

	"github.com/daewoochen/claude-code-go/internal/cli"
)

func main() {
	app := cli.NewApp(os.Stdout, os.Stderr)
	os.Exit(app.Run(context.Background(), os.Args[1:]))
}
