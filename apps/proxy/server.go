package main

import (
	"context"
	"net"
	"time"

	pkgaudit "github.com/governance-platform/pkg/audit"
	calcitepb "github.com/governance-platform/pkg/calcitepb"
	pdpv1 "github.com/governance-platform/pkg/pdpv1"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// server is the TCP listener that accepts PG wire-protocol connections.
type server struct {
	cfg      config
	pool     *backendPool
	pipeline *rewritePipeline
	auditor  *pkgaudit.Client
	auth     *tokenAuthenticator
	log      zerolog.Logger
	quit     chan struct{}
}

func newServer(
	cfg config,
	pdpClient pdpv1.PDPClient,
	calciteClient calcitepb.CalciteRewriterClient,
	rdb *redis.Client,
	auditor *pkgaudit.Client,
	log zerolog.Logger,
) *server {
	pool := newBackendPool(cfg, log)
	pipeline := newRewritePipeline(pdpClient, calciteClient, cfg)
	auth := newTokenAuthenticator(rdb)

	return &server{
		cfg:      cfg,
		pool:     pool,
		pipeline: pipeline,
		auditor:  auditor,
		auth:     auth,
		log:      log,
		quit:     make(chan struct{}),
	}
}

// ListenAndServe starts the PG wire-protocol TCP listener.
func (s *server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.ProxyAddr)
	if err != nil {
		return err
	}
	s.log.Info().Str("addr", s.cfg.ProxyAddr).Msg("proxy listening for PG connections")

	// Orphan connection sweeper.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.pool.SweepOrphans()
			case <-s.quit:
				return
			}
		}
	}()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				s.log.Error().Err(err).Msg("accept error")
				continue
			}
		}
		go s.handleConn(ctx, conn)
	}
}

// Shutdown drains in-flight connections and closes pools.
func (s *server) Shutdown() {
	close(s.quit)
	s.pool.Close()
}

func (s *server) handleConn(ctx context.Context, conn net.Conn) {
	c := newProxyConn(conn, s.pool, s.pipeline, s.auditor, s.auth, s.cfg, s.log)
	c.Serve(ctx)
}
