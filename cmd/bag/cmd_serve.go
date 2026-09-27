package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/rytsh/bag/internal/config"
	"github.com/rytsh/bag/internal/server"
)

func init() {
	register(command{name: "serve", usage: "serve the graph over MCP (stdio or http)", run: runServe})
}

func runServe(ctx context.Context, args []string) error {
	cfg := config.From(ctx)

	fs := newFlags("serve")
	gp := fs.String("graph", "", "path to graph.json")
	transport := fs.String("transport", "stdio", "stdio or http")
	host := fs.String("host", cfg.Server.Host, "HTTP bind host")
	port := fs.String("port", cfg.Server.Port, "HTTP bind port")
	path := fs.String("path", cfg.Server.Path, "HTTP MCP mount path")
	apiKey := fs.String("api-key", cfg.Server.APIKey, "require Authorization: Bearer <key> (or X-API-Key)")
	stateless := fs.Bool("stateless", false, "stateless MCP sessions (load-balanced deployments)")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	target := *gp
	if target == "" && len(pos) > 0 {
		target = pos[0]
	}

	store, err := server.NewStore(resolveGraphPath(ctx, target))
	if err != nil {
		return fmt.Errorf("load graph; %w", err)
	}

	switch *transport {
	case "stdio":
		// stdout carries the MCP protocol; keep logs on stderr only.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

		return server.ServeStdio(ctx, store, version)
	case "http":
		if *apiKey == "" && *host != "127.0.0.1" && *host != "localhost" {
			slog.Warn("serving on a non-loopback address without --api-key")
		}

		return server.ServeHTTP(ctx, store, server.HTTPOptions{
			Addr: *host + ":" + *port, Path: *path, APIKey: *apiKey, Version: version, Stateles: *stateless,
		})
	default:
		return fmt.Errorf("unknown transport %q", *transport)
	}
}
