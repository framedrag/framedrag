package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"framedrag.dev/framedrag/internal/config"
	"framedrag.dev/framedrag/internal/target"
)

func newServeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve file target lists over loopback HTTP",
		Long: "Serve is the companion for URL table aliases: it serves each file\n" +
			"target's lists read-only on its configured serve address, so the\n" +
			"firewall can point a URL table alias at\n" +
			"http://127.0.0.1:<port>/<alias>.txt. Run 'framedrag update' first to\n" +
			"write the lists; serve blocks until SIGINT or SIGTERM.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runServe(cmd.Context())
		},
	}
}

func (a *app) runServe(ctx context.Context) error {
	cfgPath, err := a.resolveConfigPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var servers []target.Server
	var addrs []string
	for i, t := range cfg.Targets {
		if t.Type != "file" || t.Serve == "" {
			continue
		}
		var opts []target.Option
		if t.AllowNonLoopback {
			opts = append(opts, target.AllowNonLoopback())
		}
		tg, err := target.NewFile(t.Dir, t.Serve, opts...)
		if err != nil {
			return fmt.Errorf("target %d: %w", i, err)
		}
		srv, ok := tg.(target.Server)
		if !ok {
			return fmt.Errorf("target %d: file target does not support serving", i)
		}
		servers = append(servers, srv)
		addrs = append(addrs, t.Serve+" (lists from "+t.Dir+")")
	}
	if len(servers) == 0 {
		return errors.New("no file target with a serve address in config; add serve: 127.0.0.1:<port> to a file target")
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	for _, addr := range addrs {
		fmt.Fprintf(a.stderr, "serving on %s\n", addr)
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func(s target.Server) { errc <- s.Serve(ctx) }(s)
	}
	var firstErr error
	for range servers {
		if err := <-errc; err != nil && firstErr == nil {
			firstErr = err
			stop() // shut the remaining servers down too
		}
	}
	return firstErr
}
