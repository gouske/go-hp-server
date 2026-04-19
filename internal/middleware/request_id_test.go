package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
)

// mustRequestID 는 테스트 헬퍼로 NewRequestID 를 에러 없이 만든다.
func mustRequestID(t *testing.T, opts ...RequestIDOption) func(http.Handler) http.Handler {
	t.Helper()
	mw, err := NewRequestID(opts...)
	if err != nil {
		t.Fatalf("NewRequestID: %v", err)
	}
	return mw
}

// okHandler 는 200 OK 를 반환하는 테스트용 핸들러이다.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// TS-20: 클라이언트 헤더가 없으면 응답 헤더에 32자 hex ID, ctx 에 동일 값 + generated 출처, 2회 요청 ID 상이.
func TestRequestID_GeneratesWhenNoClientHeader(t *testing.T) {
	t.Parallel()

	hexRe := regexp.MustCompile(`^[a-f0-9]{32}$`)
	var capturedID, capturedSource string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = IDFromContext(r.Context())
		capturedSource = string(SourceFromContext(r.Context()))
	})
	mw := mustRequestID(t)

	first := httptest.NewRecorder()
	mw(h).ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
	id1 := first.Header().Get(HeaderRequestID)
	if !hexRe.MatchString(id1) {
		t.Fatalf("1st X-Request-ID 형식 불일치: %q", id1)
	}
	if id1 != capturedID {
		t.Fatalf("1st ctx ID != header: ctx=%q header=%q", capturedID, id1)
	}
	if capturedSource != string(IDSourceGenerated) {
		t.Fatalf("1st source=%q want=generated", capturedSource)
	}

	second := httptest.NewRecorder()
	mw(h).ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	id2 := second.Header().Get(HeaderRequestID)
	if id1 == id2 {
		t.Fatalf("연속 요청 ID 중복: %q", id1)
	}
}

// TS-21: 클라이언트 ID 수용 / 거부 / 공백 trim / 다중 헤더 첫 번째 값 규칙.
func TestRequestID_AcceptsClientHeader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		headers    []string // 다중 값
		wantSource IDSource
		wantID     string // "" 이면 임의 generated
	}{
		{"정규식 통과 값 수용", []string{"abcdefgh-client-1"}, IDSourceClient, "abcdefgh-client-1"},
		{"공백 trim 후 수용", []string{"  trimmed.client-id  "}, IDSourceClient, "trimmed.client-id"},
		{"128자 초과 거부", []string{strings.Repeat("a", 129)}, IDSourceGenerated, ""},
		{"8자 미만 거부", []string{"short"}, IDSourceGenerated, ""},
		{"특수문자 거부", []string{"bad/char/value!"}, IDSourceGenerated, ""},
		{"빈 값 거부", []string{""}, IDSourceGenerated, ""},
		{"다중 헤더 첫 값 사용 (REV6-003)", []string{"primary-client-id", "bad/second"}, IDSourceClient, "primary-client-id"},
		{"다중 헤더 첫 값 거부 시 두번째 무시", []string{"bad!", "validclient.id"}, IDSourceGenerated, ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var ctxID string
			var ctxSource IDSource
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctxID = IDFromContext(r.Context())
				ctxSource = SourceFromContext(r.Context())
			})
			mw := mustRequestID(t)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			for _, v := range tc.headers {
				req.Header.Add(HeaderRequestID, v)
			}
			rr := httptest.NewRecorder()
			mw(h).ServeHTTP(rr, req)

			if ctxSource != tc.wantSource {
				t.Fatalf("source=%q want=%q", ctxSource, tc.wantSource)
			}
			if tc.wantID != "" && ctxID != tc.wantID {
				t.Fatalf("id=%q want=%q", ctxID, tc.wantID)
			}
			if rr.Header().Get(HeaderRequestID) != ctxID {
				t.Fatalf("응답 헤더 != ctx ID: header=%q ctx=%q", rr.Header().Get(HeaderRequestID), ctxID)
			}
		})
	}
}

// TS-27 (a): ID 생성 실패 주입 → 500, 에러 로그 1건, next.ServeHTTP 미호출.
func TestRequestID_GeneratorFailureReturns500(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	l := zerolog.New(&buf)

	var nextCalls int32
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&nextCalls, 1)
	})

	mw := mustRequestID(t,
		WithIDGenerator(func() (string, error) { return "", errors.New("boom") }),
		WithErrorLogger(&l),
	)

	rr := httptest.NewRecorder()
	mw(next).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=500", rr.Code)
	}
	if got := atomic.LoadInt32(&nextCalls); got != 0 {
		t.Fatalf("next.ServeHTTP 호출됨: %d회", got)
	}
	if !strings.Contains(buf.String(), "middleware request_id") || !strings.Contains(buf.String(), "boom") {
		t.Fatalf("에러 로그 기대 문구 누락: %q", buf.String())
	}
}

// TS-27 (b): 클라이언트 ID 가 정상 제공되면 생성기 미호출 → 200.
func TestRequestID_FailingGeneratorNotCalledWhenClientIDValid(t *testing.T) {
	t.Parallel()

	mw := mustRequestID(t,
		WithIDGenerator(func() (string, error) { return "", errors.New("should not be called") }),
		WithErrorLogger(silentLogger()),
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "validclient.id-1")
	rr := httptest.NewRecorder()
	mw(okHandler()).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=200", rr.Code)
	}
}

// TS-32: NewRequestID(WithIDGenerator(nil)) → (nil, error), 메시지에 "id generator must not be nil".
func TestNewRequestID_WithNilIDGeneratorReturnsError(t *testing.T) {
	t.Parallel()

	mw, err := NewRequestID(WithIDGenerator(nil))
	if err == nil {
		t.Fatalf("nil generator 에 대해 에러 반환 기대")
	}
	if mw != nil {
		t.Fatalf("에러 시 mw=nil 기대")
	}
	if !strings.Contains(err.Error(), "id generator must not be nil") {
		t.Fatalf("에러 메시지 불일치: %v", err)
	}
}

// TS-32: NewRequestID(WithErrorLogger(nil)) → (nil, error).
func TestNewRequestID_WithNilErrorLoggerReturnsError(t *testing.T) {
	t.Parallel()

	mw, err := NewRequestID(WithErrorLogger(nil))
	if err == nil {
		t.Fatalf("nil error logger 에 대해 에러 반환 기대")
	}
	if mw != nil {
		t.Fatalf("에러 시 mw=nil 기대")
	}
	if !strings.Contains(err.Error(), "error logger must not be nil") {
		t.Fatalf("에러 메시지 불일치: %v", err)
	}
}

// CR-001: NewRequestID 로 생성된 미들웨어가 next==nil 을 받아도 panic 하지 않는다.
func TestRequestID_NilNextDoesNotPanic(t *testing.T) {
	t.Parallel()

	mw := mustRequestID(t)
	handler := mw(nil) // panic 없이 404 핸들러로 대체되어야 함.

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404 (nil next fallback)", rr.Code)
	}
	if rr.Header().Get(HeaderRequestID) == "" {
		t.Fatalf("nil next 라도 X-Request-ID 는 주입되어야 함")
	}
}

// IDFromContext / SourceFromContext 바인딩 없음.
func TestRequestID_ContextAccessorsWithoutBinding(t *testing.T) {
	t.Parallel()

	if got := IDFromContext(nil); got != "" {
		t.Fatalf("IDFromContext(nil)=%q want=empty", got)
	}
	if got := SourceFromContext(nil); got != "" {
		t.Fatalf("SourceFromContext(nil)=%q want=empty", got)
	}
}

// silentLogger 는 출력을 버리는 zerolog 를 반환한다.
func silentLogger() *zerolog.Logger {
	l := zerolog.New(io.Discard)
	return &l
}
