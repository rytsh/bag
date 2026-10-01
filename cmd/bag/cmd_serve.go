package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/rytsh/bag/internal/config"
	"github.com/rytsh/bag/internal/pipeline"
	"github.com/rytsh/bag/internal/server"
)

func init() {
	register(command{name: "serve", usage: "serve the graph over MCP (stdio or http)", run: runServe})
}

func runServe(ctx context.Context, args []string) error {
	cfg := config.From(ctx)

	fs := newFlags("serve")
	gp := fs.String("graph", "", "path to graph.json")
	root := fs.String("root", ".", "fixed project root for stdio graph builds")
	readOnly := fs.Bool("read-only", false, "disable stdio extract_graph/update_graph (HTTP is always read-only)")
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

	var store *server.Store
	if *transport == "stdio" && !*readOnly {
		projectRoot, rootErr := filepath.Abs(*root)
		if rootErr != nil {
			return fmt.Errorf("resolve project root; %w", rootErr)
		}
		if target == "" {
			target = filepath.Join(pipeline.OutDir(projectRoot, cfg.OutDir), "graph.json")
		} else {
			target = resolveGraphPath(ctx, target)
		}
		store, err = server.NewProjectStore(target, pipeline.Options{
			Root: projectRoot, Gitignore: true, Workers: cfg.Workers, Resolution: 1.0,
		})
	} else {
		store, err = server.NewStore(resolveGraphPath(ctx, target))
	}
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
