package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/logger"
)

// captureLogger 는 bytes.Buffer 로 zerolog 출력을 캡처한다 (동시성 안전은 테스트 내부 보장).
func captureLogger() (*zerolog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	l := zerolog.New(&buf)
	return &l, &buf
}

// mustAccessLog 는 NewAccessLog 를 에러 없이 생성한다.
func mustAccessLog(t *testing.T, base *zerolog.Logger) func(http.Handler) http.Handler {
	t.Helper()
	mw, err := NewAccessLog(base)
	if err != nil {
		t.Fatalf("NewAccessLog: %v", err)
	}
	return mw
}

// parseLogLine 은 bytes.Buffer 내의 마지막 완전한 JSON 로그 라인을 map 으로 반환한다.
func parseLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		t.Fatalf("로그 라인 없음: %q", buf.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &m); err != nil {
		t.Fatalf("로그 JSON 파싱 실패: %v (line=%q)", err, lines[len(lines)-1])
	}
	return m
}

// TS-22: AccessLog 고정 필드 8개 모두 존재.
func TestAccessLog_FixedFields(t *testing.T) {
	t.Parallel()

	l, buf := captureLogger()
	reqMW := mustRequestID(t)
	logMW := mustAccessLog(t, l)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chained := Chain(h, reqMW, logMW)

	req := httptest.NewRequest(http.MethodPost, "/users?x=1", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set(HeaderRequestID, "abcdefgh-client-1")
	chained.ServeHTTP(httptest.NewRecorder(), req)

	entry := parseLogLine(t, buf)
	for _, key := range []string{
		"request_id", "request_id_source", "method", "path",
		"status", "duration_ms", "bytes_written", "remote_addr",
	} {
		if _, ok := entry[key]; !ok {
			t.Errorf("필드 누락: %q (entry=%v)", key, entry)
		}
	}
	if entry["request_id_source"] != string(IDSourceClient) {
		t.Errorf("source=%v want=client", entry["request_id_source"])
	}
	if entry["method"] != http.MethodPost {
		t.Errorf("method=%v", entry["method"])
	}
	// REV-FINAL-003: path 는 쿼리 제외.
	if entry["path"] != "/users" {
		t.Errorf("path=%v want=/users (쿼리 제외)", entry["path"])
	}
	if entry["remote_addr"] != "127.0.0.1:12345" {
		t.Errorf("remote_addr=%v", entry["remote_addr"])
	}
}

// TS-22 보강: request_id / request_id_source 가 로그 라인에 정확히 1회만 등장해야 한다.
// reqLogger 의 With() 바인딩 + emitAccessLog 의 Str() 이 중복되던 버그 회귀 테스트.
func TestAccessLog_FieldsAppearExactlyOnce(t *testing.T) {
	t.Parallel()

	l, buf := captureLogger()
	reqMW := mustRequestID(t)
	logMW := mustAccessLog(t, l)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	Chain(h, reqMW, logMW).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	raw := strings.TrimSpace(buf.String())
	for _, key := range []string{`"request_id":`, `"request_id_source":`} {
		if c := strings.Count(raw, key); c != 1 {
			t.Errorf("필드 %q 등장 횟수=%d want=1 (raw=%s)", key, c, raw)
		}
	}
}

// TS-23: 상태/바이트 집계.
func TestAccessLog_StatusAndBytes(t *testing.T) {
	t.Parallel()

	l, buf := captureLogger()
	reqMW := mustRequestID(t)
	logMW := mustAccessLog(t, l)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		if _, err := w.Write([]byte("pot")); err != nil {
			t.Errorf("Write: %v", err)
		}
	})
	Chain(h, reqMW, logMW).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	entry := parseLogLine(t, buf)
	if entry["status"].(float64) != 418 {
		t.Errorf("status=%v want=418", entry["status"])
	}
	if entry["bytes_written"].(float64) != 3 {
		t.Errorf("bytes_written=%v want=3", entry["bytes_written"])
	}
}

// TS-24: panic 경로에서도 AccessLog 1회 출력 + status=0 + panic 재전파.
func TestAccessLog_PanicIsRethrownAndLogged(t *testing.T) {
	t.Parallel()

	l, buf := captureLogger()
	reqMW := mustRequestID(t)
	logMW := mustAccessLog(t, l)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	chained := Chain(h, reqMW, logMW)

	func() {
		defer func() {
			rec := recover()
			if rec == nil {
				t.Fatalf("panic 재전파되지 않음")
			}
			if rec != "boom" {
				t.Fatalf("panic 값 변형됨: %v", rec)
			}
		}()
		chained.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()

	entry := parseLogLine(t, buf)
	if entry["status"].(float64) != 0 {
		t.Errorf("status=%v want=0", entry["status"])
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Errorf("로그 라인 수=%d want=1 (lines=%v)", len(lines), lines)
	}
}

// TS-26: race 하에서 100개 동시 요청 → 로그 라인 100개, 각 request_id 유일.
func TestAccessLog_RaceAndUniqueIDs(t *testing.T) {
	t.Parallel()

	// 동시 write 는 thread-safe writer 가 필요하므로 sync.Mutex 로 감싼 버퍼를 사용한다.
	var mu sync.Mutex
	var buf bytes.Buffer
	l := zerolog.New(&syncWriter{mu: &mu, w: &buf})

	reqMW := mustRequestID(t)
	logMW := mustAccessLog(t, &l)

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chained := Chain(h, reqMW, logMW)

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			chained.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != N {
		t.Fatalf("로그 라인 수=%d want=%d", len(lines), N)
	}
	seen := make(map[string]struct{}, N)
	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("JSON 파싱 실패: %v", err)
		}
		id, _ := m["request_id"].(string)
		if id == "" {
			t.Fatalf("request_id 누락: %q", line)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("request_id 중복: %q", id)
		}
		seen[id] = struct{}{}
	}
}

// syncWriter 는 zerolog 출력의 동시 쓰기를 직렬화한다.
type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// TS-28: 핸들러 내부에서 logger.FromContext 로 요청 스코프 로거 조회 + request_id 자동 포함.
func TestAccessLog_InjectsRequestScopedLogger(t *testing.T) {
	t.Parallel()

	base, buf := captureLogger()
	reqMW := mustRequestID(t)
	logMW := mustAccessLog(t, base)

	var ctxLoggerOK bool
	var ctxLoggerNonNil bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l, ok := logger.FromContext(r.Context())
		ctxLoggerOK = ok
		ctxLoggerNonNil = l != nil
		if l != nil {
			l.Info().Msg("work")
		}
	})
	Chain(h, reqMW, logMW).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if !ctxLoggerOK || !ctxLoggerNonNil {
		t.Fatalf("FromContext 실패: ok=%v non-nil=%v", ctxLoggerOK, ctxLoggerNonNil)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("로그 라인 수=%d want=2 (핸들러 + access)", len(lines))
	}
	var handlerLine, accessLine map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &handlerLine); err != nil {
		t.Fatalf("handler line JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &accessLine); err != nil {
		t.Fatalf("access line JSON: %v", err)
	}
	if handlerLine["request_id"] == "" || handlerLine["request_id"] != accessLine["request_id"] {
		t.Fatalf("핸들러 로그 request_id 불일치: handler=%v access=%v",
			handlerLine["request_id"], accessLine["request_id"])
	}
	if handlerLine["message"] != "work" {
		t.Fatalf("handler message=%v want=work", handlerLine["message"])
	}
}

// CR-001: NewAccessLog 로 생성된 미들웨어가 next==nil 을 받아도 panic 하지 않는다.
func TestAccessLog_NilNextDoesNotPanic(t *testing.T) {
	t.Parallel()

	l, _ := captureLogger()
	logMW := mustAccessLog(t, l)
	handler := logMW(nil) // panic 없이 404 핸들러로 대체.

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404", rr.Code)
	}
}

// TS-31: NewAccessLog(nil) → (nil, error), panic 없음.
func TestNewAccessLog_NilBaseReturnsError(t *testing.T) {
	t.Parallel()

	mw, err := NewAccessLog(nil)
	if mw != nil {
		t.Fatalf("mw=nil 기대")
	}
	if err == nil {
		t.Fatalf("에러 반환 기대")
	}
	if !strings.Contains(err.Error(), "access_log: base logger must not be nil") {
		t.Fatalf("에러 메시지 불일치: %v", err)
	}
}

// TS-33 (a): duration_ms 가 소수 3자리로 반올림된다.
func TestAccessLog_DurationIsRoundedTo3Decimals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   float64
		want float64
	}{
		{1.234, 1.234},
		{1.2345, 1.235}, // 표준 반올림 (round-half-to-even → 1.234 이 될 수 있으나 math.Round 는 half-away-from-zero)
		{0.0009, 0.001},
		{0.0004, 0.0},
	}
	for _, tc := range cases {
		if got := roundTo3Decimals(tc.in); got != tc.want {
			t.Errorf("roundTo3Decimals(%v)=%v want=%v", tc.in, got, tc.want)
		}
	}
}

// 핸들러가 여러 번 Write 해도 로그 라인은 1회.
func TestAccessLog_SingleLogLinePerRequest(t *testing.T) {
	t.Parallel()

	l, buf := captureLogger()
	reqMW := mustRequestID(t)
	logMW := mustAccessLog(t, l)

	var called int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
		if _, err := w.Write([]byte("a")); err != nil {
			t.Errorf("Write 1: %v", err)
		}
		if _, err := w.Write([]byte("bc")); err != nil {
			t.Errorf("Write 2: %v", err)
		}
	})
	Chain(h, reqMW, logMW).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := atomic.LoadInt32(&called); got != 1 {
		t.Fatalf("handler 호출=%d want=1", got)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("로그 라인 수=%d want=1", len(lines))
	}
}

// BenchmarkAccessLog 는 NFR-020 검증용 (ns/op < 50,000).
func BenchmarkAccessLog(b *testing.B) {
	l := zerolog.New(io.Discard)
	reqMW, err := NewRequestID(WithErrorLogger(&l))
	if err != nil {
		b.Fatal(err)
	}
	logMW, err := NewAccessLog(&l)
	if err != nil {
		b.Fatal(err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chained := Chain(h, reqMW, logMW)
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		chained.ServeHTTP(rr, req)
	}
}
