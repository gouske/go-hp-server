// Package middleware 의 RequestTimeout 테스트는 P0-4 의 TS-63 ~ TS-65a 를 커버한다.
package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
)

// cfgWithRequestTimeout 는 Apply 테스트용 최소 Config 를 만든다.
func cfgWithRequestTimeout(d time.Duration) *config.Config {
	c := &config.Config{}
	c.Server.RequestTimeout = d
	return c
}

// TestRequestTimeoutConfig_NewRejectsNonPositive 는 TS-65: 0/음수 초기값을 거부하는지 본다.
func TestRequestTimeoutConfig_NewRejectsNonPositive(t *testing.T) {
	t.Parallel()
	for _, d := range []time.Duration{0, -1 * time.Second} {
		if _, err := NewRequestTimeoutConfig(d); err == nil {
			t.Errorf("NewRequestTimeoutConfig(%s) err = nil, want error", d)
		}
	}
}

// TestRequestTimeoutConfig_NameAndApply 는 Name 과 Apply 의 기본 동작을 검증한다.
func TestRequestTimeoutConfig_NameAndApply(t *testing.T) {
	t.Parallel()
	cfg, err := NewRequestTimeoutConfig(30 * time.Second)
	if err != nil {
		t.Fatalf("NewRequestTimeoutConfig: %v", err)
	}
	if cfg.Name() != "request_timeout" {
		t.Errorf("Name() = %q, want %q", cfg.Name(), "request_timeout")
	}
	if err := cfg.Apply(context.Background(), cfgWithRequestTimeout(50*time.Millisecond)); err != nil {
		t.Fatalf("Apply() err = %v, want nil", err)
	}
	// Apply 는 0/음수를 거부한다(검증 우회 방지).
	if err := cfg.Apply(context.Background(), cfgWithRequestTimeout(0)); err == nil {
		t.Error("Apply(0) err = nil, want error")
	}
}

// TestRequestTimeout_AppliesInitialTimeout 는 TS-63: 협조적 핸들러가 ctx 데드라인에서
// 조기 복귀하는지 검증한다.
func TestRequestTimeout_AppliesInitialTimeout(t *testing.T) {
	t.Parallel()
	cfg, err := NewRequestTimeoutConfig(100 * time.Millisecond)
	if err != nil {
		t.Fatalf("NewRequestTimeoutConfig: %v", err)
	}

	var observedErr error
	var elapsed time.Duration
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		select {
		case <-r.Context().Done():
			observedErr = r.Context().Err()
		case <-time.After(2 * time.Second):
		}
		elapsed = time.Since(start)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	RequestTimeout(cfg)(h).ServeHTTP(rec, req)

	if !errors.Is(observedErr, context.DeadlineExceeded) {
		t.Errorf("ctx err = %v, want context.DeadlineExceeded", observedErr)
	}
	if elapsed > time.Second {
		t.Errorf("handler took %s, expected ~100ms (ctx deadline 준수)", elapsed)
	}
}

// TestRequestTimeout_AppliesUpdatedTimeout 는 TS-64: Apply 후 새 타임아웃이 적용되는지 본다.
func TestRequestTimeout_AppliesUpdatedTimeout(t *testing.T) {
	t.Parallel()
	cfg, err := NewRequestTimeoutConfig(5 * time.Second)
	if err != nil {
		t.Fatalf("NewRequestTimeoutConfig: %v", err)
	}
	if err := cfg.Apply(context.Background(), cfgWithRequestTimeout(50*time.Millisecond)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var observedErr error
	var elapsed time.Duration
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		select {
		case <-r.Context().Done():
			observedErr = r.Context().Err()
		case <-time.After(2 * time.Second):
		}
		elapsed = time.Since(start)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	RequestTimeout(cfg)(h).ServeHTTP(rec, req)

	if !errors.Is(observedErr, context.DeadlineExceeded) {
		t.Errorf("ctx err = %v, want context.DeadlineExceeded", observedErr)
	}
	if elapsed > time.Second {
		t.Errorf("handler took %s, expected ~50ms after Apply", elapsed)
	}
}

// TestRequestTimeout_DoesNotAbortUncooperativeHandler 는 TS-65a(REV4-003 호출자 계약):
// ctx 를 무시하는 핸들러를 강제 종료하지 않고, 504 자동 응답도 하지 않는지 검증한다.
func TestRequestTimeout_DoesNotAbortUncooperativeHandler(t *testing.T) {
	t.Parallel()
	cfg, err := NewRequestTimeoutConfig(50 * time.Millisecond)
	if err != nil {
		t.Fatalf("NewRequestTimeoutConfig: %v", err)
	}

	var elapsed time.Duration
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		time.Sleep(200 * time.Millisecond) // ctx 무시
		elapsed = time.Since(start)
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	RequestTimeout(cfg)(h).ServeHTTP(rec, req)

	if elapsed < 200*time.Millisecond {
		t.Errorf("handler 는 끝까지 실행되어야 함(~200ms), got %s", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (강제 504 없음)", rec.Code)
	}
}

// TestRequestTimeoutConfig_Apply_NilConfig 는 nil Config 시 에러를 반환하는지 본다.
func TestRequestTimeoutConfig_Apply_NilConfig(t *testing.T) {
	t.Parallel()
	cfg, err := NewRequestTimeoutConfig(time.Second)
	if err != nil {
		t.Fatalf("NewRequestTimeoutConfig: %v", err)
	}
	if err := cfg.Apply(context.Background(), nil); err == nil {
		t.Error("Apply(nil) err = nil, want error")
	}
}

// TestRequestTimeout_NilNextHandler 는 next==nil 시 panic 없이 404 로 대체하는지 본다.
func TestRequestTimeout_NilNextHandler(t *testing.T) {
	t.Parallel()
	cfg, err := NewRequestTimeoutConfig(time.Second)
	if err != nil {
		t.Fatalf("NewRequestTimeoutConfig: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	RequestTimeout(cfg)(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (next==nil → NotFound)", rec.Code)
	}
}

// TestRequestTimeout_NilConfigPassthrough 는 cfg==nil 시 panic 없이 통과시키는지 본다.
func TestRequestTimeout_NilConfigPassthrough(t *testing.T) {
	t.Parallel()
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	RequestTimeout(nil)(h).ServeHTTP(rec, req)
	if !called {
		t.Error("nil config 시에도 next 핸들러는 호출되어야 함(panic-free passthrough)")
	}
}
