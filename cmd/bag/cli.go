package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// command is one CLI subcommand.
type command struct {
	name  string
	usage string
	run   func(ctx context.Context, args []string) error
}

var errUsage = errors.New("usage")

var commands []command

func register(c command) { commands = append(commands, c) }

func lookup(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}

	return nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "bag - codebase knowledge graphs (Graphify-compatible)\n\nUsage:\n  bag <command> [flags]\n\nCommands:\n")

	for _, c := range commands {
		fmt.Fprintf(os.Stderr, "  %-12s %s\n", c.name, c.usage)
	}

	fmt.Fprintln(os.Stderr, "\nRun `bag <command> -h` for command flags.")
}

// newFlags creates a flag set that allows flags after positional args.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	return fs
}

// parseInterspersed parses flags that may appear after positional args.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string

	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}

		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}

		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}

		pos = append(pos, args[0])
		args = args[1:]

		if len(args) == 0 {
			return pos, nil
		}

		if !strings.HasPrefix(args[0], "-") {
			for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
				pos = append(pos, args[0])
				args = args[1:]
			}

			if len(args) == 0 {
				return pos, nil
			}
		}
	}
}
