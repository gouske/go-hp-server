package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
)

// RequestTimeoutConfig 는 RequestTimeout 미들웨어가 참조하는 atomic 저장소이다.
//
// 핸들러 실행 deadline(ns)을 atomic.Int64 로 보관하며, reload.Subscriber 를 함께 구현해
// Apply 시 lock-free 로 값을 갱신한다(NFR-061). 요청 처리 경로는 atomic Load 만 하므로
// mutex 가 없다.
type RequestTimeoutConfig struct {
	timeoutNanos atomic.Int64
}

// NewRequestTimeoutConfig 는 초기 deadline 을 저장한 RequestTimeoutConfig 를 반환한다.
// d <= 0 이면 `middleware: request_timeout` 접두 에러를 반환하며 panic 하지 않는다.
func NewRequestTimeoutConfig(d time.Duration) (*RequestTimeoutConfig, error) {
	if d <= 0 {
		return nil, fmt.Errorf("middleware: request_timeout must be > 0, got %s", d)
	}
	c := &RequestTimeoutConfig{}
	c.timeoutNanos.Store(int64(d))
	return c, nil
}

// Name 은 Subscriber 식별자 "request_timeout" 을 반환한다.
func (c *RequestTimeoutConfig) Name() string {
	return "request_timeout"
}

// Apply 는 cfg.Server.RequestTimeout 을 atomic Store 한다(reload.Subscriber).
// cfg 가 nil 이거나 RequestTimeout <= 0 이면 검증 우회를 막기 위해 에러를 반환하고
// 기존 값을 유지한다. panic 하지 않는다.
func (c *RequestTimeoutConfig) Apply(_ context.Context, cfg *config.Config) error {
	if cfg == nil {
		return errors.New("middleware: request_timeout apply: nil config")
	}
	d := cfg.Server.RequestTimeout
	if d <= 0 {
		return fmt.Errorf("middleware: request_timeout apply: must be > 0, got %s", d)
	}
	c.timeoutNanos.Store(int64(d))
	return nil
}

// load 는 현재 deadline 을 atomic 으로 읽어 반환한다.
func (c *RequestTimeoutConfig) load() time.Duration {
	return time.Duration(c.timeoutNanos.Load())
}

// RequestTimeout 은 요청마다 최신 deadline 을 atomic 으로 읽어 context.WithTimeout 을
// 적용하는 미들웨어를 반환한다.
//
// 호출자 계약 (P0-4 FR-060g / REV4-003 B안): 본 미들웨어는 ctx.Done() 을 따르지 않는
// 핸들러를 강제 중단하지 않는다. 핸들러는 반드시 ctx 취소에 협조해야 하며, 그렇지 않으면
// 타임아웃 보장이 없다. 504 Gateway Timeout 자동 응답 같은 추가 방어선은 P2-11(Recovery/
// Timeout 미들웨어)에서 재평가한다.
//
// panic-free 계약: cfg == nil 이면 deadline 없이 next 로 통과시키고, next == nil 이면
// http.NotFoundHandler 로 대체한다.
func RequestTimeout(cfg *RequestTimeoutConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if next == nil {
			next = http.NotFoundHandler()
		}
		if cfg == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), cfg.load())
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
