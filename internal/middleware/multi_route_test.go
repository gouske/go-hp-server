package middleware

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
)

// TS-34 (REV6-001): ServeMux longest-match 로 인해 `/` 체인 1회만으로는
// `/health` 등 더 구체적 경로가 미들웨어를 우회할 수 있다.
// 모든 라우트를 register 헬퍼로 등록하면 각 경로마다 체인이 적용되어 누락이 발생하지 않는다.
func TestMultiRoute_RegisterHelperAppliesChainPerRoute(t *testing.T) {
	t.Parallel()

	// bytes.Buffer 를 동시 쓰기 안전하게 감싼다.
	var mu sync.Mutex
	var buf bytes.Buffer
	base := zerolog.New(&syncWriter{mu: &mu, w: &buf})

	reqMW, err := NewRequestID(WithErrorLogger(&base))
	if err != nil {
		t.Fatalf("NewRequestID: %v", err)
	}
	logMW, err := NewAccessLog(&base)
	if err != nil {
		t.Fatalf("NewAccessLog: %v", err)
	}

	// register 헬퍼는 cmd/server/main.go 의 동등 구조를 재현한다.
	mux := http.NewServeMux()
	register := func(pattern string, h http.Handler) {
		mux.Handle(pattern, Chain(h, reqMW, logMW))
	}

	register("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, "root"); err != nil {
			t.Errorf("Fprint root: %v", err)
		}
	}))
	register("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, "ok"); err != nil {
			t.Errorf("Fprint health: %v", err)
		}
	}))

	for _, path := range []string{"/", "/health"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d want=200", path, rr.Code)
		}
		if rr.Header().Get(HeaderRequestID) == "" {
			t.Fatalf("path=%s 응답에 X-Request-ID 헤더 누락", path)
		}
	}

	mu.Lock()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	mu.Unlock()
	if len(lines) != 2 {
		t.Fatalf("access 로그 라인 수=%d want=2 (/, /health)", len(lines))
	}
	paths := map[string]bool{}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("JSON 파싱 실패: %v", err)
		}
		if m["request_id"] == nil || m["request_id_source"] == nil {
			t.Fatalf("필수 필드 누락: %v", m)
		}
		paths[m["path"].(string)] = true
	}
	if !paths["/"] || !paths["/health"] {
		t.Fatalf("두 경로 로그 모두 존재해야 함: %v", paths)
	}
}

// 대조 실험: register 헬퍼를 우회하고 raw 핸들러를 직접 등록하면
// 응답 헤더에 X-Request-ID 가 없고 access 로그도 없다.
// 이는 FR-032 의 "모든 srv.Handle 호출에 register 강제" 규칙의 필요성을 증명한다.
func TestMultiRoute_RawHandlerBypassesMiddleware(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("/raw", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/raw", nil))

	if rr.Header().Get(HeaderRequestID) != "" {
		t.Fatalf("raw 등록 경로에 X-Request-ID 가 있음 (미들웨어 적용됨) — register 헬퍼가 필요 없는 상태")
	}
}
