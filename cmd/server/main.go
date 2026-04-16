// main 은 고성능 서버의 진입점이다.
//
// P0-1 단계에서는 설정 로드 → 검증 → zerolog 부트스트랩까지만 수행하고
// 즉시 정상 종료한다. HTTP 리스너와 graceful shutdown 은 P0-2 에서 추가된다.
//
// 설정 로드/검증/로거 초기화 중 하나라도 실패하면 non-zero exit code 로
// 종료하며 실패 원인은 stderr 에 JSON 로그 한 줄로 출력한다. panic 은
// 사용하지 않는다.
package main

import (
	"flag"
	"os"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
	"github.com/gouske/go-hp-server/internal/logger"
)

// exitCodeOK 는 정상 종료, exitCodeError 는 초기화 실패 시 사용하는 종료 코드이다.
const (
	exitCodeOK    = 0
	exitCodeError = 1
)

// main 은 run 의 반환값을 프로세스 종료 코드로 전달한다.
// 본문에 로직을 두지 않아 run 에서 defer 가 정상적으로 실행되도록 한다.
func main() {
	os.Exit(run(os.Args[1:]))
}

// run 은 CLI 인자를 받아 초기화 파이프라인을 수행하고 종료 코드를 반환한다.
// 테스트 용이성을 위해 os.Args 의존을 main 에만 남긴다.
func run(args []string) int {
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

	lg.Info().
		Str("host", cfg.Server.Host).
		Int("port", cfg.Server.Port).
		Msg("server skeleton initialized")
	return exitCodeOK
}

// bootstrapLogger 는 설정/로거 초기화 이전에 사용할 최소 zerolog 인스턴스를 반환한다.
// 설정에 의존하지 않고 고정된 형식으로 stderr 에 출력한다.
// zerolog 의 레벨 메서드(Error/Info/...)는 포인터 리시버이므로 포인터로 반환한다.
func bootstrapLogger() *zerolog.Logger {
	lg := zerolog.New(os.Stderr).With().Timestamp().Logger()
	return &lg
}
