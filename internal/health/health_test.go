package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// captureLogger 는 bytes.Buffer 기반 동시 쓰기 안전 로거를 반환한다.
func captureLogger() (*zerolog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	w := &syncWriter{w: &buf}
	l := zerolog.New(w)
	return &l, &buf
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// mustReadiness 는 테스트 전용 생성 헬퍼.
func mustReadiness(t *testing.T, opts ...ReadinessOption) http.Handler {
	t.Helper()
	h, err := Readiness(opts...)
	if err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	return h
}

// doRequest 는 헬스 요청을 실행해 응답 상태코드/헤더/바디를 반환한다.
func doRequest(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
	return rr
}

// parseReadiness 는 /ready 응답 JSON 을 구조체로 파싱한다.
func parseReadiness(t *testing.T, rr *httptest.ResponseRecorder) readinessBody {
	t.Helper()
	var body readinessBody
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("response JSON parse: %v (raw=%q)", err, rr.Body.String())
	}
	return body
}

// ---------------------------------------------------------------------------
// Liveness (TS-40 ~ TS-42)
// ---------------------------------------------------------------------------

// TS-40: GET /health → 200 + Content-Type + Cache-Control + {"status":"ok"}.
func TestLiveness_GET(t *testing.T) {
	t.Parallel()

	rr := doRequest(t, Liveness(), http.MethodGet, "/health")

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type=%q want=application/json", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q want=no-store", got)
	}
	if got := strings.TrimSpace(rr.Body.String()); got != `{"status":"ok"}` {
		t.Errorf("body=%q want={\"status\":\"ok\"}", got)
	}
}

// TS-41 (a): HEAD /health → 200 + JSON Content-Type, 바디 비어 있음.
// TS-41 (b): POST /health → 405 + Allow: GET, HEAD.
func TestLiveness_Method(t *testing.T) {
	t.Parallel()

	t.Run("HEAD", func(t *testing.T) {
		t.Parallel()
		rr := doRequest(t, Liveness(), http.MethodHead, "/health")
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d want=200", rr.Code)
		}
		if got := rr.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type=%q", got)
		}
		if rr.Body.Len() != 0 {
			t.Errorf("HEAD 바디가 비어있어야 함: %q", rr.Body.String())
		}
	})

	t.Run("POST", func(t *testing.T) {
		t.Parallel()
		rr := doRequest(t, Liveness(), http.MethodPost, "/health")
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want=405", rr.Code)
		}
		if got := rr.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow=%q want=GET, HEAD", got)
		}
	})
}

// TS-42: 100 goroutine 동시 요청, 모두 200, -race 통과.
func TestLiveness_Concurrent(t *testing.T) {
	t.Parallel()

	h := Liveness()
	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
			if rr.Code != http.StatusOK {
				t.Errorf("status=%d", rr.Code)
			}
		}()
	}
	wg.Wait()
}

// BenchmarkLiveness 는 NFR-040 검증용 (평균 ns/op < 10,000).
func BenchmarkLiveness(b *testing.B) {
	h := Liveness()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// CR-001 회귀: 405 경로에서도 Cache-Control: no-store 가 포함되어야 한다.
func TestLivenessAndReadiness_405HaveCacheControl(t *testing.T) {
	t.Parallel()

	readiness, err := Readiness()
	if err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	for _, tc := range []struct {
		name string
		h    http.Handler
	}{
		{"Liveness", Liveness()},
		{"Readiness", readiness},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rr := doRequest(t, tc.h, http.MethodPost, "/")
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status=%d want=405", rr.Code)
			}
			if got := rr.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("405 Cache-Control=%q want=no-store", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Readiness 정상 경로 (TS-43 ~ TS-45)
// ---------------------------------------------------------------------------

// TS-43: 체커 0개 → 200 + status=ok + checks=[].
func TestReadiness_EmptyCheckers(t *testing.T) {
	t.Parallel()

	h := mustReadiness(t)
	rr := doRequest(t, h, http.MethodGet, "/ready")

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=200", rr.Code)
	}
	body := parseReadiness(t, rr)
	if body.Status != "ok" {
		t.Errorf("status=%q want=ok", body.Status)
	}
	if len(body.Checks) != 0 {
		t.Errorf("checks len=%d want=0", len(body.Checks))
	}
}

// TS-44: 모든 체커 성공 → 200 + 등록 순서 유지.
func TestReadiness_AllCheckersPass(t *testing.T) {
	t.Parallel()

	ok := func(ctx context.Context) error { return nil }
	h := mustReadiness(t,
		WithChecker("db", ok),
		WithChecker("cache", ok),
	)
	rr := doRequest(t, h, http.MethodGet, "/ready")

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", rr.Code, rr.Body.String())
	}
	body := parseReadiness(t, rr)
	if body.Status != "ok" {
		t.Errorf("status=%q want=ok", body.Status)
	}
	if len(body.Checks) != 2 {
		t.Fatalf("checks len=%d want=2", len(body.Checks))
	}
	// FR-042b: 등록 순서 유지.
	if body.Checks[0].Name != "db" || body.Checks[1].Name != "cache" {
		t.Errorf("order mismatch: %v", body.Checks)
	}
	for _, c := range body.Checks {
		if !c.OK || c.Error != nil {
			t.Errorf("check %q not ok: %+v", c.Name, c)
		}
	}
}

// TS-45: 일부 체커 실패 → 503 + status=degraded + 응답 error="failed" + 서버 로거에 원본 기록.
func TestReadiness_PartialFailureMasksError(t *testing.T) {
	t.Parallel()

	lg, buf := captureLogger()
	h := mustReadiness(t,
		WithChecker("db", func(ctx context.Context) error { return nil }),
		WithChecker("cache", func(ctx context.Context) error { return errors.New("boom") }),
		WithErrorLogger(lg),
	)
	rr := doRequest(t, h, http.MethodGet, "/ready")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=503 body=%s", rr.Code, rr.Body.String())
	}
	body := parseReadiness(t, rr)
	if body.Status != "degraded" {
		t.Errorf("status=%q want=degraded", body.Status)
	}
	if body.Checks[1].OK || body.Checks[1].Error == nil || *body.Checks[1].Error != string(ErrCodeFailed) {
		t.Errorf("cache check error 필드 불일치: %+v", body.Checks[1])
	}
	// 보안 정책: 원본 "boom" 이 외부 응답 바디에는 나타나지 않음.
	if strings.Contains(rr.Body.String(), "boom") {
		t.Errorf("원본 에러가 외부 응답에 노출됨: %s", rr.Body.String())
	}
	// 로거에는 원본이 기록됨.
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("로거에 원본 에러가 기록되지 않음: %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Readiness 실패·타임아웃·panic (TS-46 ~ TS-47)
// ---------------------------------------------------------------------------

// TS-46: ctx 무시 블로킹 체커 + 짧은 check timeout → 응답은 타임아웃 내 503 반환.
func TestReadiness_TimeoutWithBlockingChecker(t *testing.T) {
	t.Parallel()

	lg, _ := captureLogger()
	blockingDone := make(chan struct{})
	t.Cleanup(func() { <-blockingDone })

	h := mustReadiness(t,
		WithCheckTimeout(20*time.Millisecond),
		WithErrorLogger(lg),
		WithChecker("slow", func(ctx context.Context) error {
			// 의도적으로 ctx 무시. 테스트 종료 전 정리되도록 sleep 짧게.
			time.Sleep(100 * time.Millisecond)
			close(blockingDone)
			return nil
		}),
	)

	start := time.Now()
	rr := doRequest(t, h, http.MethodGet, "/ready")
	elapsed := time.Since(start)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=503", rr.Code)
	}
	if elapsed > 80*time.Millisecond {
		t.Errorf("응답 지연=%v, 타임아웃 내 복귀 기대", elapsed)
	}
	body := parseReadiness(t, rr)
	if body.Checks[0].Error == nil || *body.Checks[0].Error != string(ErrCodeTimeout) {
		t.Errorf("error 코드=%v want=timeout", body.Checks[0].Error)
	}
}

// TS-47: 체커 panic → 503 + error="panic", 원본 상세 문자열은 응답에 노출 안됨.
func TestReadiness_PanicIsRecoveredAndMasked(t *testing.T) {
	t.Parallel()

	lg, buf := captureLogger()
	h := mustReadiness(t,
		WithChecker("bad", func(ctx context.Context) error {
			panic("secret-details-should-not-leak")
		}),
		WithErrorLogger(lg),
	)

	rr := doRequest(t, h, http.MethodGet, "/ready")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=503", rr.Code)
	}
	body := parseReadiness(t, rr)
	if body.Checks[0].Error == nil || *body.Checks[0].Error != string(ErrCodePanic) {
		t.Errorf("error=%v want=panic", body.Checks[0].Error)
	}
	if strings.Contains(rr.Body.String(), "secret-details") {
		t.Errorf("panic 상세가 외부 응답에 노출됨: %s", rr.Body.String())
	}
	if !strings.Contains(buf.String(), "secret-details") {
		t.Errorf("panic 상세가 로거에 기록되지 않음: %s", buf.String())
	}

	// 후속 요청이 영향받지 않음을 확인.
	rr2 := doRequest(t, h, http.MethodGet, "/ready")
	if rr2.Code != http.StatusServiceUnavailable {
		t.Errorf("2nd status=%d", rr2.Code)
	}
}

// ---------------------------------------------------------------------------
// 옵션 에러 계약 (TS-48 ~ TS-50)
// ---------------------------------------------------------------------------

// TS-48: 이름 중복 → (nil, error).
func TestReadiness_DuplicateCheckerName(t *testing.T) {
	t.Parallel()

	ok := func(ctx context.Context) error { return nil }
	h, err := Readiness(WithChecker("db", ok), WithChecker("db", ok))
	if h != nil {
		t.Fatalf("h 는 nil 이어야 함")
	}
	if err == nil || !strings.Contains(err.Error(), "duplicate checker name") {
		t.Fatalf("에러 메시지 불일치: %v", err)
	}
}

// TS-49: 빈 이름 / nil fn / nil ErrorLogger → 각각 (nil, error), panic 없음.
func TestReadiness_InvalidCheckerOptions(t *testing.T) {
	t.Parallel()

	ok := func(ctx context.Context) error { return nil }
	cases := []struct {
		name string
		opt  ReadinessOption
		want string
	}{
		{"empty name", WithChecker("", ok), "checker name must not be empty"},
		{"nil fn", WithChecker("db", nil), "checker fn must not be nil"},
		{"nil error logger", WithErrorLogger(nil), "error logger must not be nil"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := Readiness(tc.opt)
			if h != nil {
				t.Fatalf("h 는 nil 이어야 함")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("에러 불일치: want~%q got=%v", tc.want, err)
			}
		})
	}
}

// TS-50: 잘못된 타임아웃 → (nil, error).
func TestReadiness_InvalidTimeouts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opt  ReadinessOption
	}{
		{"check 0", WithCheckTimeout(0)},
		{"check neg", WithCheckTimeout(-1 * time.Second)},
		{"total 0", WithTotalTimeout(0)},
		{"total neg", WithTotalTimeout(-1 * time.Second)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, err := Readiness(tc.opt)
			if h != nil || err == nil {
				t.Fatalf("에러 반환 기대, got h=%v err=%v", h, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// /ready 메서드 + 인코딩 실패 (TS-51 ~ TS-53)
// ---------------------------------------------------------------------------

// TS-51: HEAD /ready → 200, 바디 비어 있음, 체커는 실행됨.
func TestReadiness_HEAD(t *testing.T) {
	t.Parallel()

	var called int32
	h := mustReadiness(t, WithChecker("db", func(ctx context.Context) error {
		atomic.AddInt32(&called, 1)
		return nil
	}))

	rr := doRequest(t, h, http.MethodHead, "/ready")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=200", rr.Code)
	}
	if rr.Body.Len() != 0 {
		t.Errorf("HEAD 바디=%q want=empty", rr.Body.String())
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Errorf("체커 호출=%d want=1 (HEAD 에서도 평가)", called)
	}
}

// TS-52: POST /ready → 405 + Allow: GET, HEAD, 체커 미호출.
func TestReadiness_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	var called int32
	h := mustReadiness(t, WithChecker("db", func(ctx context.Context) error {
		atomic.AddInt32(&called, 1)
		return nil
	}))

	rr := doRequest(t, h, http.MethodPost, "/ready")
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d want=405", rr.Code)
	}
	if got := rr.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow=%q", got)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("405 경로에서 체커가 호출됨: %d", called)
	}
}

// failingWriter 는 Write 가 항상 에러를 반환하는 ResponseWriter 더블이다.
type failingWriter struct {
	header http.Header
	status int
}

func (f *failingWriter) Header() http.Header {
	if f.header == nil {
		f.header = make(http.Header)
	}
	return f.header
}
func (f *failingWriter) WriteHeader(s int)               { f.status = s }
func (f *failingWriter) Write(p []byte) (int, error)     { return 0, errors.New("write failed") }

// TS-53: Encode 실패 시 WithErrorLogger 로 기록, panic 없음.
func TestReadiness_EncodeFailureLogged(t *testing.T) {
	t.Parallel()

	lg, buf := captureLogger()
	h := mustReadiness(t, WithErrorLogger(lg))

	fw := &failingWriter{}
	h.ServeHTTP(fw, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if fw.status != http.StatusOK {
		t.Errorf("status=%d want=200", fw.status)
	}
	if !strings.Contains(buf.String(), "response encode failed") {
		t.Errorf("인코딩 실패 로그가 기록되지 않음: %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Singleflight (TS-54 ~ TS-55)
// ---------------------------------------------------------------------------

// TS-54: 성공 체커 + 동시 요청 10개 → 모두 200, 체커 실행 카운터=1.
func TestReadiness_Singleflight_Success(t *testing.T) {
	t.Parallel()

	var called int64
	h := mustReadiness(t,
		WithCheckTimeout(500*time.Millisecond),
		WithChecker("db", func(ctx context.Context) error {
			atomic.AddInt64(&called, 1)
			time.Sleep(50 * time.Millisecond)
			return nil
		}),
	)

	const N = 10
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			rr := doRequest(t, h, http.MethodGet, "/ready")
			if rr.Code != http.StatusOK {
				t.Errorf("status=%d want=200 body=%s", rr.Code, rr.Body.String())
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&called); got != 1 {
		t.Errorf("체커 호출=%d want=1 (singleflight 공유)", got)
	}

	// inflight 해제 확인: 별도 요청에서 카운터가 2 로 증가해야 함.
	rr := doRequest(t, h, http.MethodGet, "/ready")
	if rr.Code != http.StatusOK {
		t.Errorf("2nd status=%d", rr.Code)
	}
	if got := atomic.LoadInt64(&called); got != 2 {
		t.Errorf("2nd 체커 호출=%d want=2", got)
	}
}

// TS-55: ctx 무시 블로킹 체커 + 2라운드 10개씩 동시 요청 → 모두 503/timeout, 카운터=2.
// 라운드 간 경계는 "블로킹 시간 경과 + inflight 해제 확인" polling 으로 보장.
func TestReadiness_Singleflight_TimeoutBurst(t *testing.T) {
	t.Parallel()

	const blockDuration = 100 * time.Millisecond
	var called int64
	lg, _ := captureLogger()
	h := mustReadiness(t,
		WithCheckTimeout(20*time.Millisecond),
		WithErrorLogger(lg),
		WithChecker("slow", func(ctx context.Context) error {
			atomic.AddInt64(&called, 1)
			// ctx 를 의도적으로 무시 (호출자 계약 위반 시나리오).
			time.Sleep(blockDuration)
			return nil
		}),
	)

	burst := func(round int) {
		const N = 10
		var wg sync.WaitGroup
		wg.Add(N)
		for i := 0; i < N; i++ {
			go func() {
				defer wg.Done()
				rr := doRequest(t, h, http.MethodGet, "/ready")
				if rr.Code != http.StatusServiceUnavailable {
					t.Errorf("round=%d status=%d want=503", round, rr.Code)
				}
				body := parseReadiness(t, rr)
				if body.Checks[0].Error == nil || *body.Checks[0].Error != string(ErrCodeTimeout) {
					t.Errorf("round=%d error=%v want=timeout", round, body.Checks[0].Error)
				}
			}()
		}
		wg.Wait()
	}

	burst(1)
	if got := atomic.LoadInt64(&called); got != 1 {
		t.Errorf("round1 체커 호출=%d want=1", got)
	}

	// inflight 해제 보장: 체커 블로킹이 끝나고 실행 고루틴이 close(done) + inflight=nil
	// 복귀할 시점까지 물리적으로 대기. blockDuration + 여유.
	time.Sleep(blockDuration + 50*time.Millisecond)

	burst(2)
	if got := atomic.LoadInt64(&called); got != 2 {
		t.Errorf("round2 체커 호출=%d want=2 (singleflight 공유)", got)
	}
}

// ---------------------------------------------------------------------------
// 추가 검증
// ---------------------------------------------------------------------------

// nil 옵션은 조용히 건너뛴다.
func TestReadiness_NilOptionSkipped(t *testing.T) {
	t.Parallel()

	var nilOpt ReadinessOption
	h, err := Readiness(nilOpt, WithChecker("db", func(ctx context.Context) error { return nil }))
	if err != nil {
		t.Fatalf("nil 옵션으로 인해 실패하면 안됨: %v", err)
	}
	rr := doRequest(t, h, http.MethodGet, "/ready")
	if rr.Code != http.StatusOK {
		t.Errorf("status=%d", rr.Code)
	}
}

// ErrorCode 화이트리스트를 벗어난 값이 외부 응답에 노출되지 않는다.
func TestReadiness_ErrorCodeWhitelist(t *testing.T) {
	t.Parallel()

	// context.DeadlineExceeded → ErrCodeTimeout 매핑.
	h := mustReadiness(t,
		WithCheckTimeout(10*time.Millisecond),
		WithChecker("respects-ctx", func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	)
	rr := doRequest(t, h, http.MethodGet, "/ready")
	body := parseReadiness(t, rr)
	if body.Checks[0].Error == nil || *body.Checks[0].Error != string(ErrCodeTimeout) {
		t.Errorf("ctx.Err 는 timeout 코드로 매핑되어야 함: %v", body.Checks[0].Error)
	}
}

// 동시 요청에서 정상 체커는 degraded 로 판정되지 않는다 (FR-042a 불변식).
func TestReadiness_NormalCheckerNeverDegradedUnderLoad(t *testing.T) {
	t.Parallel()

	h := mustReadiness(t, WithChecker("fast", func(ctx context.Context) error {
		return nil
	}))

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			rr := doRequest(t, h, http.MethodGet, "/ready")
			if rr.Code != http.StatusOK {
				t.Errorf("status=%d (정상 체커가 degraded 로 판정됨)", rr.Code)
			}
		}()
	}
	wg.Wait()
}

// BenchmarkReadiness_NoCheckers 는 NFR-041 검증용 (ns/op < 50,000).
func BenchmarkReadiness_NoCheckers(b *testing.B) {
	h, err := Readiness()
	if err != nil {
		b.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// 컴파일 참조 — fmt/io 미사용 방지용 (필요 시 제거).
var _ = fmt.Sprintf
