package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/4fuu/box/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := cli.Run(cli.Options{Args: os.Args[1:], Context: ctx})
	if err == nil {
		return
	}
	if err.Error() != "" {
		fmt.Fprintln(os.Stderr, err.Error())
	}
	os.Exit(cli.ExitCode(err))
}
