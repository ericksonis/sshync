package main

import (
	"os"

	"github.com/ericksonis/sshync/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
