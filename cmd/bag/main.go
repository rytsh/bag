package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/rakunlabs/into"
	"github.com/rakunlabs/logi"

	"github.com/rytsh/bag/internal/config"
	_ "github.com/rytsh/bag/internal/extract/langs"
)

// Injected at build time via -ldflags.
var (
	version = "v0.0.0"
	commit  = "-"
	date    = "-"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		printUsage()

		return
	}

	if os.Args[1] == "version" || os.Args[1] == "--version" {
		fmt.Printf("bag %s (commit %s, built %s)\n", version, commit, date)

		return
	}

	cmd := lookup(os.Args[1])
	if cmd == nil {
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}

	args := os.Args[2:]

	into.Init(func(ctx context.Context) error {
		cfg, err := config.Load(ctx)
		if err != nil {
			return err
		}

		ctx = config.WithContext(ctx, cfg)

		err = cmd.run(ctx, args)
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	},
		into.WithLogger(logi.InitializeLog(logi.WithCaller(false))),
		into.WithMsgf("%s %s version:[%s] commit:[%s] date:[%s]", config.ServiceName, cmd.name, version, commit, date),
		into.WithStartFn(func() {}),
		into.WithStopFn(func() {}),
		into.WithRunErrFn(func(err error) { slog.Error(err.Error()) }),
	)
}
