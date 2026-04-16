// Package logger 는 config.LogConfig 를 기반으로 zerolog 로거를 부트스트랩한다.
//
// New 는 Level/Format 검증 실패 시 panic 대신 에러를 반환하며,
// 기본 출력 대상은 os.Stdout 이다. 테스트 또는 특수 환경에서는
// WithWriter 옵션으로 출력 대상을 교체할 수 있다.
package logger

import (
	"fmt"
	"io"
	"os"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
)

// Option 은 New 동작을 조정하는 함수형 옵션 타입이다.
type Option func(*options)

// options 는 New 의 내부 옵션 집합이며 외부로 노출하지 않는다.
type options struct {
	writer io.Writer
}

// WithWriter 는 로거의 기본 출력 대상을 교체한다. 주로 테스트에서 사용한다.
// nil 을 전달하면 기본 출력 대상을 유지한다.
func WithWriter(w io.Writer) Option {
	return func(o *options) {
		if w != nil {
			o.writer = w
		}
	}
}

// New 는 LogConfig 에 따라 *zerolog.Logger 를 생성해 반환한다.
//
// Level 은 debug|info|warn|error, Format 은 json|console 중 하나여야 하며,
// 집합을 벗어나면 `logger new:` 접두 에러를 반환한다. panic 은 발생하지 않는다.
// 반환된 로거는 타임스탬프 필드를 자동으로 포함한다.
func New(cfg config.LogConfig, opts ...Option) (*zerolog.Logger, error) {
	o := &options{writer: os.Stdout}
	for _, opt := range opts {
		opt(o)
	}

	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("logger new: %w", err)
	}
	writer, err := wrapWriter(o.writer, cfg.Format)
	if err != nil {
		return nil, fmt.Errorf("logger new: %w", err)
	}

	logger := zerolog.New(writer).
		Level(level).
		With().
		Timestamp().
		Logger()
	return &logger, nil
}

// parseLevel 은 문자열 Level 을 zerolog.Level 로 변환한다.
// 허용 집합 밖이면 에러를 반환한다. 대소문자/공백 정규화는 호출자(예: config.Validate)의
// 책임이며, 본 함수는 엄격 매칭만 수행한다.
func parseLevel(level string) (zerolog.Level, error) {
	switch level {
	case "debug":
		return zerolog.DebugLevel, nil
	case "info":
		return zerolog.InfoLevel, nil
	case "warn":
		return zerolog.WarnLevel, nil
	case "error":
		return zerolog.ErrorLevel, nil
	}
	return zerolog.NoLevel, fmt.Errorf("invalid log level: %q (want debug|info|warn|error)", level)
}

// wrapWriter 는 Format 에 따라 base 출력에 ConsoleWriter 래퍼를 적용하거나 그대로 반환한다.
// 대소문자/공백 정규화는 호출자(예: config.Validate)의 책임이며, 본 함수는 엄격 매칭만 수행한다.
func wrapWriter(base io.Writer, format string) (io.Writer, error) {
	switch format {
	case "json":
		return base, nil
	case "console":
		return zerolog.ConsoleWriter{Out: base}, nil
	}
	return nil, fmt.Errorf("invalid log format: %q (want json|console)", format)
}
