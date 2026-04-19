package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TS-23 핵심 로직: 상태/바이트 집계.
func TestResponseWriter_StatusAndBytes(t *testing.T) {
	t.Parallel()

	rw := newResponseWriter(httptest.NewRecorder())
	rw.WriteHeader(http.StatusTeapot)
	if _, err := rw.Write([]byte("pot")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if rw.Status() != http.StatusTeapot {
		t.Fatalf("Status=%d want=418", rw.Status())
	}
	if rw.BytesWritten() != 3 {
		t.Fatalf("BytesWritten=%d want=3", rw.BytesWritten())
	}
}

// Write 가 WriteHeader 없이 먼저 호출되면 status=200 으로 간주.
func TestResponseWriter_WriteBeforeWriteHeader(t *testing.T) {
	t.Parallel()

	rw := newResponseWriter(httptest.NewRecorder())
	if _, err := rw.Write([]byte("a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if rw.Status() != http.StatusOK {
		t.Fatalf("Status=%d want=200", rw.Status())
	}
}

// 상태코드가 기록되지 않았으면 0 유지 (TS-24 AccessLog status=0 기반).
func TestResponseWriter_NoWriteMeansZeroStatus(t *testing.T) {
	t.Parallel()

	rw := newResponseWriter(httptest.NewRecorder())
	if rw.Status() != 0 {
		t.Fatalf("Status=%d want=0", rw.Status())
	}
}

// TS-25: Flusher / Hijacker 인터페이스 type assertion.
func TestResponseWriter_InterfaceAssertions(t *testing.T) {
	t.Parallel()

	rw := newResponseWriter(httptest.NewRecorder())

	if _, ok := http.ResponseWriter(rw).(http.Flusher); !ok {
		t.Fatalf("responseWriter 가 http.Flusher 를 구현하지 않음")
	}
	if _, ok := http.ResponseWriter(rw).(http.Hijacker); !ok {
		t.Fatalf("responseWriter 가 http.Hijacker 를 구현하지 않음")
	}
}

// CR-002: Hijack 미지원 writer 에 대해 http.ErrNotSupported 로 분기 가능해야 한다.
func TestResponseWriter_HijackReturnsErrNotSupported(t *testing.T) {
	t.Parallel()

	// httptest.NewRecorder() 는 http.Hijacker 를 구현하지 않는다.
	rw := newResponseWriter(httptest.NewRecorder())
	_, _, err := rw.Hijack()
	if err == nil {
		t.Fatalf("Hijack() 가 에러를 반환해야 함")
	}
	if !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("errors.Is(err, http.ErrNotSupported)=false, err=%v", err)
	}
}

// BenchmarkResponseWriter 는 NFR-021 검증용 (≤ 1 allocs/op).
func BenchmarkResponseWriter(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rw := newResponseWriter(httptest.NewRecorder())
		rw.WriteHeader(http.StatusOK)
		if _, err := rw.Write(nil); err != nil {
			b.Fatalf("Write: %v", err)
		}
	}
}
