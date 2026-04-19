package middleware

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

// responseWriter 는 http.ResponseWriter 를 감싸 상태코드와 쓰기 바이트 수를 집계한다.
// Flusher / Hijacker 는 원본이 지원하는 경우 그대로 패스스루한다.
// WebSocket/SSE 활용이 필요하면 필요한 인터페이스를 추가 구현해야 한다.
type responseWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

// newResponseWriter 는 주어진 http.ResponseWriter 를 감싼 래퍼를 반환한다.
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w}
}

// WriteHeader 는 처음 호출된 상태코드만 기록한다.
// 두 번째 이후 호출은 표준 라이브러리 동작에 위임한다 (경고 로그는 상위 계층 책임).
func (rw *responseWriter) WriteHeader(status int) {
	if !rw.wroteHeader {
		rw.status = status
		rw.wroteHeader = true
	}
	rw.ResponseWriter.WriteHeader(status)
}

// Write 는 쓰기 바이트 수를 집계하고 원본 writer 로 위임한다.
// WriteHeader 없이 호출되는 경우 표준 라이브러리 동작에 따라 200 OK 로 간주한다.
func (rw *responseWriter) Write(p []byte) (int, error) {
	if !rw.wroteHeader {
		rw.status = http.StatusOK
		rw.wroteHeader = true
	}
	n, err := rw.ResponseWriter.Write(p)
	rw.bytesWritten += int64(n)
	return n, err
}

// Status 는 기록된 상태코드를 반환한다. WriteHeader/Write 가 한 번도 호출되지 않았으면 0 이다.
func (rw *responseWriter) Status() int { return rw.status }

// BytesWritten 은 Write 를 통해 집계된 바이트 수를 반환한다.
func (rw *responseWriter) BytesWritten() int64 { return rw.bytesWritten }

// Flush 는 원본 writer 가 http.Flusher 를 지원하면 위임한다.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 은 원본 writer 가 http.Hijacker 를 지원하면 위임한다.
// 지원하지 않으면 http.ErrNotSupported 를 %w 로 래핑해 반환하며,
// 호출부는 errors.Is(err, http.ErrNotSupported) 로 분기할 수 있다 (CR-002).
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("middleware: hijack not supported: %w", http.ErrNotSupported)
}
