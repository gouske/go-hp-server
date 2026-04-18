// main 은 고성능 서버의 진입점이다.
//
// P0-2 단계에서는 설정 로드 → 검증 → 로거 부트스트랩 → HTTP 서버 기동 →
// SIGINT/SIGTERM 수신 시 graceful shutdown 까지 수행한다. panic 은 사용하지 않는다.
//
// 종료 코드 매핑 (FEATURE_SPEC FR-010/FR-011):
//   - 0 : 정상 종료 (ctx 정상 취소 + shutdown 완료)
//   - 1 : 초기화/Serve 실패 (config 로드·검증·로거·server.New·ErrServeFailed 등)
//   - 2 : graceful shutdown 타임아웃 (ErrShutdownTimeout)
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
	"github.com/gouske/go-hp-server/internal/logger"
	"github.com/gouske/go-hp-server/internal/server"
)

// 종료 코드 상수. AGENTS.md 의 "하드코딩 금지" 를 준수하기 위한 명명 상수.
const (
	exitCodeOK              = 0
	exitCodeError           = 1
	exitCodeShutdownTimeout = 2
)

// main 은 run 의 반환값을 프로세스 종료 코드로 전달한다.
// 본문에 로직을 두지 않아 run 에서 defer 가 정상 실행되도록 한다.
func main() {
	os.Exit(run(os.Args[1:]))
}

// run 은 OS 시그널 핸들링 책임을 맡고(FR-007),
// 실제 부트스트랩 로직은 runCore 에 위임해 테스트 가능성을 확보한다.
func run(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCore(ctx, args)
}

// runCore 는 ctx 와 CLI 인자를 받아 초기화 파이프라인 + 서버 Run 을 수행하고
// 종료 코드를 반환한다. ctx 주입형이라 signal 없이도 단위 테스트가 가능하다.
func runCore(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	configPath := fs.String("config", "config/default.yaml", "설정 YAML 파일 경로")
	if err := fs.Parse(args); err != nil {
		bootstrapLogger().Error().Err(err).Msg("flag parse failed")
		return exitCodeError
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		bootstrapLogger().Error().Err(err).Str("path", *configPath).Msg("config load failed")
		return exitCodeError
	}
	if err := cfg.Validate(); err != nil {
		bootstrapLogger().Error().Err(err).Msg("config validate failed")
		return exitCodeError
	}

	lg, err := logger.New(cfg.Log)
	if err != nil {
		bootstrapLogger().Error().Err(err).Msg("logger init failed")
		return exitCodeError
	}

	srv, err := server.New(cfg, lg)
	if err != nil {
		lg.Error().Err(err).Msg("server new failed")
		return exitCodeError
	}

	lg.Info().
		Str("host", cfg.Server.Host).
		Int("port", cfg.Server.Port).
		Msg("server starting")

	runErr := srv.Run(ctx)
	switch {
	case runErr == nil:
		lg.Info().Str("addr", srv.Addr()).Msg("server stopped gracefully")
		return exitCodeOK
	case errors.Is(runErr, server.ErrShutdownTimeout):
		lg.Error().Err(runErr).Msg("graceful shutdown timed out")
		return exitCodeShutdownTimeout
	case errors.Is(runErr, server.ErrServeFailed):
		lg.Error().Err(runErr).Msg("server serve failed")
		return exitCodeError
	default:
		lg.Error().Err(runErr).Msg("server stopped with unexpected error")
		return exitCodeError
	}
}

// bootstrapLogger 는 설정/로거 초기화 이전에 사용할 최소 zerolog 인스턴스를 반환한다.
// 설정에 의존하지 않고 고정된 형식으로 stderr 에 출력한다.
// zerolog 의 레벨 메서드(Error/Info/...)는 포인터 리시버이므로 포인터로 반환한다.
func bootstrapLogger() *zerolog.Logger {
	lg := zerolog.New(os.Stderr).With().Timestamp().Logger()
	return &lg
}
