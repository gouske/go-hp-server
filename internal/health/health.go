// Package health 는 프로세스의 liveness 와 readiness 를 표준화된 HTTP 엔드포인트로 노출한다.
//
//   - Liveness()  : `/health` 핸들러. 항상 200 OK.
//   - Readiness() : `/ready` 핸들러. 등록된 ReadinessChecker 들을 순차 실행해 판정.
//
// 설계 원칙 (FEATURE_SPEC P0-5 REV8):
//   - panic 금지 (NFR-045). 옵션 검증 실패 시 `(nil, error)` 를 반환한다.
//   - 전역 상태 금지. 로거·체커·타임아웃은 모두 옵션으로 주입한다.
//   - 체커별 singleflight 로 결과 공유 → 동시 probe 에서 체커 고루틴은 최대 1개로 상한된다.
//   - `ctx` 를 무시한 블로킹 체커 본체는 의도적으로 리크되며(호출자 계약, FR-041a),
//     프레임워크 레이어(프로미스 전파) 에서는 추가 누수를 만들지 않는다 (REV7-002).
//   - 외부 응답 `error` 필드는 요약 코드 {null, "timeout", "panic", "failed"} 만 노출하고,
//     원본 에러 메시지는 주입된 로거로만 기록한다 (FR-042 / 보안 정책).
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ErrorCode 는 /ready 응답 바디의 `checks[].error` 에 노출되는 요약 코드이다.
// 원본 에러 메시지 노출을 막기 위한 화이트리스트 (FR-042).
type ErrorCode string

// ErrorCode 값.
const (
	// ErrCodeTimeout 은 개별 또는 전체 타임아웃 만료로 체커가 실패한 경우.
	ErrCodeTimeout ErrorCode = "timeout"
	// ErrCodePanic 은 체커 함수 내부 panic 이 recover 된 경우 (FR-046).
	ErrCodePanic ErrorCode = "panic"
	// ErrCodeFailed 는 그 외 일반 에러.
	ErrCodeFailed ErrorCode = "failed"
)

// 기본값 상수.
const (
	defaultCheckTimeout = 1 * time.Second
	defaultTotalTimeout = 5 * time.Second
	contentTypeJSON     = "application/json"
	headerContentType   = "Content-Type"
	headerCacheControl  = "Cache-Control"
	headerAllow         = "Allow"
	cacheControlNoStore = "no-store"
	allowedMethods      = "GET, HEAD"
)

// ReadinessChecker 는 개별 의존성 상태를 점검하는 함수 시그니처이다.
// ctx 는 팩토리 수준 타임아웃이 적용된 상태로 전달된다.
// 에러 반환 시 해당 체커는 실패로 기록되며, 전체 /ready 응답은 503 으로 판정된다.
//
// 호출자 계약 (FR-041a):
//
//	체커 구현자는 ctx.Done() 을 주기적으로 확인하고 ctx.Err() 를 존중해야 한다.
//	계약 위반(블로킹 체커) 으로 인해 타임아웃 시 남겨지는 고루틴은 의도적으로
//	리크되며, 호출자 책임이다. Readiness 핸들러는 해당 체커의 이후 결과를 수거하지 않는다.
type ReadinessChecker func(ctx context.Context) error

// ReadinessOption 은 Readiness 팩토리의 함수형 옵션이다.
// 옵션 검증 실패 시 error 를 반환하며 Readiness 가 (nil, error) 로 전파한다.
type ReadinessOption func(*readinessConfig) error

// checkerEntry 는 등록된 체커의 상태(설정 + inflight) 를 보관한다.
type checkerEntry struct {
	name string
	fn   ReadinessChecker

	mu       sync.Mutex
	inflight *inflightEntry
}

// inflightEntry 는 singleflight 동시 실행 결과를 구독자들에게 브로드캐스트하는 구조이다.
// done 이 닫힌 뒤에는 result 필드가 불변이 되어 여러 고루틴이 안전하게 읽을 수 있다.
type inflightEntry struct {
	done   chan struct{}
	result checkResult
}

// checkResult 는 체커 1회 실행 결과이다.
type checkResult struct {
	ok      bool
	code    ErrorCode // 실패 시 요약 코드
	details string    // 내부 로그용 상세 메시지 (외부 응답엔 노출되지 않음)
}

// readinessConfig 는 Readiness 의 내부 설정이다.
type readinessConfig struct {
	entries      []*checkerEntry
	names        map[string]struct{}
	checkTimeout time.Duration
	totalTimeout time.Duration
	errorLogger  *zerolog.Logger
}

// WithChecker 는 이름과 함께 체커를 등록한다.
//   - name == "" 또는 fn == nil 이면 에러 반환.
//   - 동일 이름 중복 등록 시 에러 반환 ("duplicate checker name").
func WithChecker(name string, fn ReadinessChecker) ReadinessOption {
	return func(c *readinessConfig) error {
		if name == "" {
			return errors.New("health: checker name must not be empty")
		}
		if fn == nil {
			return errors.New("health: checker fn must not be nil")
		}
		if _, dup := c.names[name]; dup {
			return fmt.Errorf("health: duplicate checker name %q", name)
		}
		c.names[name] = struct{}{}
		c.entries = append(c.entries, &checkerEntry{name: name, fn: fn})
		return nil
	}
}

// WithCheckTimeout 은 개별 체커 호출당 적용되는 context 타임아웃을 설정한다.
// d <= 0 이면 에러 반환. 미지정 시 기본 1s.
func WithCheckTimeout(d time.Duration) ReadinessOption {
	return func(c *readinessConfig) error {
		if d <= 0 {
			return fmt.Errorf("health: check timeout must be > 0, got %v", d)
		}
		c.checkTimeout = d
		return nil
	}
}

// WithTotalTimeout 은 /ready 핸들러 1회 호출의 전체 응답 상한을 설정한다.
// 체커 수가 많을 때 누적 지연을 방지한다.
// d <= 0 이면 에러 반환. 미지정 시 기본 5s.
func WithTotalTimeout(d time.Duration) ReadinessOption {
	return func(c *readinessConfig) error {
		if d <= 0 {
			return fmt.Errorf("health: total timeout must be > 0, got %v", d)
		}
		c.totalTimeout = d
		return nil
	}
}

// WithErrorLogger 는 Readiness 에 한해 다음 두 가지 에러를 기록할 로거를 주입한다:
//
//	(1) 응답 JSON 인코딩 실패 (FR-048)
//	(2) 개별 체커의 상세 실패 원인 — 외부 응답에는 요약 코드만 노출되므로
//	    원본 에러 메시지는 여기로만 기록된다 (FR-042 보안 정책).
//
// l == nil 이면 에러 반환. 미지정 시 zerolog.Nop().
func WithErrorLogger(l *zerolog.Logger) ReadinessOption {
	return func(c *readinessConfig) error {
		if l == nil {
			return errors.New("health: error logger must not be nil")
		}
		c.errorLogger = l
		return nil
	}
}

// Liveness 는 프로세스가 응답 가능한지만 판정하는 핸들러를 반환한다.
//
// GET/HEAD: 200 OK + application/json + Cache-Control: no-store + {"status":"ok"}
// (HEAD 는 http 표준 동작에 의해 바디가 자동으로 버려진다.)
// 그 외 메서드: 405 + Allow: GET, HEAD.
func Liveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CR-001: 405 경로도 캐시 방지 (FR-040a).
		w.Header().Set(headerCacheControl, cacheControlNoStore)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set(headerAllow, allowedMethods)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set(headerContentType, contentTypeJSON)
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		// Liveness 는 로거 주입 없이 best-effort 로 동작한다 (FR-048):
		// 고정 문자열 쓰기 실패는 runtime 에러 외엔 발생하지 않으므로, 에러를 로깅하지 않고
		// 조용히 복귀한다. CR-002 에 따라 `_` 대신 명시적 에러 체크 후 return 한다.
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			return
		}
	})
}

// Readiness 는 등록된 체커를 순차 실행해 트래픽 수용 준비 여부를 판정하는 핸들러를 반환한다.
// 옵션 검증 실패 시 (nil, error) 를 반환한다.
// 응답 헤더에 Cache-Control: no-store 를 항상 포함한다 (FR-040a).
func Readiness(opts ...ReadinessOption) (http.Handler, error) {
	cfg := &readinessConfig{
		names:        make(map[string]struct{}),
		checkTimeout: defaultCheckTimeout,
		totalTimeout: defaultTotalTimeout,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}
	if cfg.errorLogger == nil {
		nop := zerolog.Nop()
		cfg.errorLogger = &nop
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CR-001: 405 경로도 캐시 방지 (FR-040a).
		w.Header().Set(headerCacheControl, cacheControlNoStore)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set(headerAllow, allowedMethods)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		// 전체 응답 상한 적용.
		ctx, cancel := context.WithTimeout(r.Context(), cfg.totalTimeout)
		defer cancel()

		results := runAllCheckers(ctx, cfg)

		w.Header().Set(headerContentType, contentTypeJSON)

		allOK := true
		for _, res := range results {
			if !res.OK {
				allOK = false
				break
			}
		}
		status := http.StatusOK
		body := readinessBody{Status: "ok", Checks: results}
		if !allOK {
			status = http.StatusServiceUnavailable
			body.Status = "degraded"
		}
		w.WriteHeader(status)
		if r.Method == http.MethodHead {
			// HEAD 는 바디 생략. 체커 실행은 이미 수행되었으므로 운영 의미(헬스체크) 는 유지.
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			cfg.errorLogger.Error().Err(err).Msg("health readiness: response encode failed")
		}
	}), nil
}

// readinessBody 는 /ready 응답 JSON 의 최상위 스키마이다.
type readinessBody struct {
	Status string          `json:"status"`
	Checks []checkResponse `json:"checks"`
}

// checkResponse 는 개별 체커의 외부 응답 스키마이다.
// Error 는 ErrorCode 화이트리스트 문자열 또는 nil 만 허용한다.
type checkResponse struct {
	Name  string  `json:"name"`
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
}

// runAllCheckers 는 설정된 모든 체커를 등록 순서대로 실행하고 외부 응답 배열을 반환한다.
// FR-042b: 배열 순서는 WithChecker 등록 순서를 유지한다.
func runAllCheckers(ctx context.Context, cfg *readinessConfig) []checkResponse {
	out := make([]checkResponse, 0, len(cfg.entries))
	for _, entry := range cfg.entries {
		// 전체 타임아웃이 먼저 만료되면 남은 체커는 미실행 타임아웃으로 기록 (FR-044b).
		if err := ctx.Err(); err != nil {
			code := string(ErrCodeTimeout)
			out = append(out, checkResponse{Name: entry.name, OK: false, Error: &code})
			cfg.errorLogger.Warn().
				Str("checker", entry.name).
				Msg("health readiness: total timeout expired before checker started")
			continue
		}

		res := runOne(ctx, entry, cfg.checkTimeout, cfg.errorLogger)
		item := checkResponse{Name: entry.name, OK: res.ok}
		if !res.ok {
			code := string(res.code)
			item.Error = &code
		}
		out = append(out, item)
	}
	return out
}

// runOne 은 단일 체커를 singleflight 패턴으로 실행하고 결과를 반환한다.
//
// 타임아웃 계층 (FR-041 / FR-044b):
//   - 각 요청은 checkTimeout 로 제한된 "대기 ctx" 로 entry.done 을 기다린다.
//     따라서 singleflight 를 공유하는 구독자끼리도 자신의 개별 타임아웃으로
//     복귀할 수 있다 (예: 첫 실행이 블로킹 중이어도 후속 요청은 checkTimeout 후 반환).
//   - 실행 고루틴 내부의 체커 호출은 별도 ctx (executeChecker 가 생성) 를 사용한다.
//     즉 체커 본체의 ctx 와 구독자들의 대기 ctx 는 분리된다.
//   - parentCtx 는 /ready 핸들러의 totalTimeout ctx 로, 이것이 먼저 만료되면
//     대기 ctx 도 함께 만료된다 (자식 ctx 의 부모 취소 전파).
//
// 체커 고루틴은 결과 저장 + close(done) 후 자연 종료되며, 이후 inflight 는 nil 로 복귀한다.
func runOne(parentCtx context.Context, entry *checkerEntry, checkTimeout time.Duration, errLg *zerolog.Logger) checkResult {
	// per-request wait ctx: 구독자(첫 실행자 포함) 는 본인 checkTimeout 으로 대기한다.
	waitCtx, waitCancel := context.WithTimeout(parentCtx, checkTimeout)
	defer waitCancel()

	// singleflight 진입: 이미 실행 중이면 동일 entry 를 공유.
	entry.mu.Lock()
	if entry.inflight != nil {
		ent := entry.inflight
		entry.mu.Unlock()
		return awaitInflight(waitCtx, entry.name, ent, errLg)
	}
	ent := &inflightEntry{done: make(chan struct{})}
	entry.inflight = ent
	entry.mu.Unlock()

	go func() {
		ent.result = executeChecker(entry, checkTimeout, errLg)
		close(ent.done)
		entry.mu.Lock()
		entry.inflight = nil
		entry.mu.Unlock()
	}()

	return awaitInflight(waitCtx, entry.name, ent, errLg)
}

// awaitInflight 는 inflight 실행 완료 또는 parent ctx 취소를 대기한다.
// parent ctx 취소 시 타임아웃 결과만 반환하고 실행 고루틴은 계속 돌도록 둔다 (FR-041a/041b).
func awaitInflight(parentCtx context.Context, name string, ent *inflightEntry, errLg *zerolog.Logger) checkResult {
	select {
	case <-ent.done:
		return ent.result
	case <-parentCtx.Done():
		errLg.Warn().
			Str("checker", name).
			Err(parentCtx.Err()).
			Msg("health readiness: timed out waiting for checker result")
		return checkResult{ok: false, code: ErrCodeTimeout, details: parentCtx.Err().Error()}
	}
}

// executeChecker 는 체커 함수를 checkTimeout ctx 로 호출하고 panic 을 recover 한다.
// 반환값은 inflightEntry.result 로 저장되어 모든 구독자가 동일하게 수신한다.
func executeChecker(entry *checkerEntry, checkTimeout time.Duration, errLg *zerolog.Logger) (res checkResult) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	defer func() {
		if rec := recover(); rec != nil {
			details := fmt.Sprintf("checker panic: %v", rec)
			errLg.Error().
				Str("checker", entry.name).
				Interface("panic", rec).
				Msg("health readiness: checker panicked")
			res = checkResult{ok: false, code: ErrCodePanic, details: details}
		}
	}()

	if err := entry.fn(ctx); err != nil {
		// ctx 만료로 인한 에러 vs 체커 자체 에러 구분.
		code := ErrCodeFailed
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			code = ErrCodeTimeout
		}
		errLg.Warn().
			Str("checker", entry.name).
			Err(err).
			Msg("health readiness: checker failed")
		return checkResult{ok: false, code: code, details: err.Error()}
	}
	return checkResult{ok: true}
}
