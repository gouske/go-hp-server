package middleware

import (
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/logger"
)

// NewAccessLog 는 요청 완료 시점에 고정 스키마로 info 레벨 로그를 1회 출력하는
// 미들웨어 팩토리를 반환한다. base 는 명시적으로 주입된 로거이며 nil 을 허용하지 않는다.
// base == nil 이면 (nil, error) 를 반환한다. panic 은 발생하지 않는다 (NFR-025).
//
// 동작 (FR-024 ~ FR-026, FR-031):
//   - 요청 진입 시점에 base 로부터 request_id / request_id_source 필드를 바인딩한
//     요청 스코프 로거를 생성해 logger.WithContext 로 ctx 에 주입한다.
//   - 완료 시점 (defer) 에 info 레벨로 1회 로그를 출력한다. 필드는 FR-025 참조.
//   - 핸들러 panic 시 recover 하지 않고 재전파하되, defer 로 로그는 반드시 기록된다.
//     상태코드가 기록되지 않은 경우 status=0.
//   - 미들웨어 체인 순서상 NewRequestID 이후에 위치해야 request_id/source 가 주입된다.
func NewAccessLog(base *zerolog.Logger) (func(http.Handler) http.Handler, error) {
	if base == nil {
		return nil, errors.New("middleware: access_log: base logger must not be nil")
	}

	return func(next http.Handler) http.Handler {
		// CR-001: next == nil 입력에 대한 panic-free 방어.
		if next == nil {
			next = http.NotFoundHandler()
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := IDFromContext(r.Context())
			source := SourceFromContext(r.Context())

			// 요청 스코프 로거를 만들어 ctx 에 주입 (FR-031).
			reqLogger := base.With().
				Str("request_id", id).
				Str("request_id_source", string(source)).
				Logger()
			ctx := logger.WithContext(r.Context(), &reqLogger)

			rw := newResponseWriter(w)

			// panic 재전파 + defer 로 완료 로그 1회 보장 (FR-026).
			defer func() {
				rec := recover()
				emitAccessLog(&reqLogger, r, rw, start, id, source)
				if rec != nil {
					panic(rec)
				}
			}()

			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}, nil
}

// emitAccessLog 는 AccessLog 미들웨어가 출력하는 완료 로그 1줄을 기록한다.
// 별도 함수로 분리해 defer + 일반 종료 경로 모두에서 동일 포맷을 보장한다.
//
// 전달된 l 은 request_id / request_id_source 가 이미 With() 로 바인딩된
// 요청 스코프 로거이므로, 이 함수에서는 해당 필드를 중복 기록하지 않는다.
// (id, source 인자는 시그니처 호환성 유지 목적이며 내부 검증에만 사용한다.)
func emitAccessLog(l *zerolog.Logger, r *http.Request, rw *responseWriter, start time.Time, _ string, _ IDSource) {
	duration := time.Since(start)
	durationMS := roundTo3Decimals(float64(duration) / float64(time.Millisecond))

	l.Info().
		Str("method", r.Method).
		Str("path", r.URL.Path). // REV-FINAL-003: 쿼리스트링 제외.
		Int("status", rw.Status()).
		Float64("duration_ms", durationMS).
		Int64("bytes_written", rw.BytesWritten()).
		Str("remote_addr", r.RemoteAddr).
		Msg("request completed")
}

// roundTo3Decimals 는 반올림 자리수 3 으로 반올림한 float64 를 반환한다 (FR-025 duration_ms).
func roundTo3Decimals(v float64) float64 {
	return math.Round(v*1000) / 1000
}
