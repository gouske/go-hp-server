package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func recordingMW(name string, rec *[]string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*rec = append(*rec, "enter:"+name)
			next.ServeHTTP(w, r)
			*rec = append(*rec, "leave:"+name)
		})
	}
}

// Chain(h, a, b) 의 요청 시점 실행 순서는 a → b → h 여야 한다.
func TestChain_ExecutionOrder(t *testing.T) {
	t.Parallel()

	var rec []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec = append(rec, "handler")
		fmt.Fprint(w, "ok")
	})
	chained := Chain(h, recordingMW("A", &rec), recordingMW("B", &rec))

	rr := httptest.NewRecorder()
	chained.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	got := strings.Join(rec, ",")
	want := "enter:A,enter:B,handler,leave:B,leave:A"
	if got != want {
		t.Fatalf("order mismatch:\n got = %s\nwant = %s", got, want)
	}
}

// REV6-002: mws 가 비면 h 를 그대로 반환한다.
func TestChain_NoMiddlewaresReturnsHandler(t *testing.T) {
	t.Parallel()

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	out := Chain(h)

	// 동일 핸들러여야 한다 (래핑 없음).
	if fmt.Sprintf("%p", out) != fmt.Sprintf("%p", http.Handler(h)) {
		t.Fatalf("Chain(h) 가 h 를 그대로 반환하지 않음")
	}
}

// REV6-002: mws 중 nil 요소는 건너뛴다 (panic 없음).
func TestChain_SkipsNilMiddlewares(t *testing.T) {
	t.Parallel()

	var rec []string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec = append(rec, "handler")
	})
	chained := Chain(h, nil, recordingMW("A", &rec), nil)

	rr := httptest.NewRecorder()
	chained.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	got := strings.Join(rec, ",")
	want := "enter:A,handler,leave:A"
	if got != want {
		t.Fatalf("nil skip 실패: got=%s want=%s", got, want)
	}
}

// REV6-002: h == nil 이면 http.NotFoundHandler() 로 대체 (panic 없음).
func TestChain_NilHandlerReplacedWithNotFound(t *testing.T) {
	t.Parallel()

	out := Chain(nil)
	rr := httptest.NewRecorder()
	out.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("nil handler fallback 실패: status=%d want=404", rr.Code)
	}
}
