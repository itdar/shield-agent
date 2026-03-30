package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"rua/internal/auth"
	"rua/internal/middleware"
	"rua/internal/monitor"
	"rua/internal/storage"
	"rua/internal/telemetry"
	"rua/internal/transport/proxy"
)

// buildProxyCmd는 `shield-agent proxy` 서브커맨드를 생성한다.
func buildProxyCmd(flags *globalFlags) *cobra.Command {
	var (
		listenAddr    string
		upstream      string
		transportType string
	)

	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "HTTP 프록시 모드: MCP 클라이언트와 업스트림 MCP 서버 사이에 위치",
		Long: `Claude Desktop 같은 MCP 클라이언트와 업스트림 MCP 서버(fastmcp 등) 사이에
HTTP 프록시 서버를 실행한다. 인증(AuthMiddleware)과 로깅(LogMiddleware)이 중간에 적용된다.

지원 transport:
  sse             — Server-Sent Events (GET /sse + POST /messages)
  streamable-http — Streamable HTTP (POST /mcp)

예시 (로컬 fastmcp SSE):
  shield-agent proxy --listen :8888 --upstream http://localhost:8000 --transport sse

예시 (클라우드 MCP Streamable HTTP):
  shield-agent proxy --listen :8888 --upstream https://mcp.example.com --transport streamable-http`,

		RunE: func(cmd *cobra.Command, args []string) error {
			return runProxy(cmd.Context(), flags, listenAddr, upstream, transportType)
		},
		SilenceUsage: true,
	}

	f := cmd.Flags()
	f.StringVar(&listenAddr, "listen", ":8888", "리슨 주소 (예: :8888 또는 127.0.0.1:8888)")
	f.StringVar(&upstream, "upstream", "", "업스트림 MCP 서버 base URL (필수, 예: http://localhost:8000)")
	f.StringVar(&transportType, "transport", "streamable-http", "transport 종류: sse 또는 streamable-http")
	_ = cmd.MarkFlagRequired("upstream")

	return cmd
}

// runProxy는 proxy 모드의 메인 실행 로직이다.
func runProxy(ctx context.Context, flags *globalFlags, listenAddr, upstream, transportType string) error {
	cfg, logger, err := initFromFlags(flags)
	if err != nil {
		return err
	}

	logger.Info("starting shield-agent proxy",
		"transport", transportType,
		"listen", listenAddr,
		"upstream", upstream,
	)

	// 1. DB 초기화.
	db, err := storage.Open(cfg.Storage.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	if n, err := db.Purge(cfg.Storage.RetentionDays); err == nil && n > 0 {
		logger.Info("purged old log entries", "count", n)
	}

	// 2. Prometheus 메트릭.
	metrics := monitor.NewMetrics(monitor.DefaultRegisterer())

	// 3. Auth KeyStore.
	fileStore, err := auth.NewFileKeyStore(cfg.Security.KeyStorePath)
	if err != nil {
		return fmt.Errorf("loading key store: %w", err)
	}
	cachedStore := auth.NewCachedKeyStore(fileStore, 5*time.Minute)
	authMW := middleware.NewAuthMiddleware(cachedStore, cfg.Security.Mode, logger, func(status string) {
		metrics.AuthTotal.WithLabelValues(status).Inc()
	})

	// 4. 텔레메트리.
	telCol := telemetry.New(
		cfg.Telemetry.Enabled,
		cfg.Telemetry.Endpoint,
		cfg.Telemetry.BatchInterval,
		cfg.Telemetry.Epsilon,
		"",
		logger,
	)

	// 5. 로그 미들웨어.
	logMW := middleware.NewLogMiddleware(db, logger, telCol)
	defer logMW.Close()

	chain := middleware.NewChain(authMW, logMW)

	// 6. 모니터링 서버 시작.
	monSrv := monitor.New(cfg.Server.MonitorAddr, metrics, logger)
	monSrv.Start()

	// 7. 텔레메트리 백그라운드 실행.
	telCtx, telCancel := context.WithCancel(ctx)
	defer telCancel()
	go telCol.Run(telCtx)

	// 8. transport 핸들러 선택.
	var handler http.Handler
	switch transportType {
	case "sse":
		handler = proxy.NewSSEProxy(upstream, chain, logger).Handler()
	case "streamable-http", "streamable_http", "http":
		handler = proxy.NewStreamableProxy(upstream, chain, logger).Handler()
	default:
		return fmt.Errorf("unknown transport %q — use sse or streamable-http", transportType)
	}

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      handler,
		ReadTimeout:  0, // SSE는 timeout 없음
		WriteTimeout: 0,
	}

	// 9. context 취소 시 graceful shutdown.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		monSrv.Shutdown(shutCtx)
		srv.Shutdown(shutCtx) //nolint:errcheck
	}()

	printBanner(cfg.Security.Mode, cfg.Server.MonitorAddr, "proxy("+transportType+")")
	logger.Info("proxy listening", "addr", listenAddr, "transport", transportType)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("proxy server: %w", err)
	}
	return nil
}
