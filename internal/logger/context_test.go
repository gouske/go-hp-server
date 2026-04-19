package logger

import (
	"context"
	"io"
	"testing"

	"github.com/rs/zerolog"
)

// TS-29: FromContext 바인딩 없음 → (nil, false).
func TestFromContext_NoBinding(t *testing.T) {
	t.Parallel()

	got, ok := FromContext(context.Background())
	if ok {
		t.Fatalf("FromContext ok=true, want false")
	}
	if got != nil {
		t.Fatalf("FromContext logger=%v, want nil", got)
	}
}

// TS-30: WithContext → FromContext 왕복 시 동일 포인터 반환 + ok=true.
func TestWithContext_RoundTrip(t *testing.T) {
	t.Parallel()

	l := zerolog.New(io.Discard)
	ctx := WithContext(context.Background(), &l)

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatalf("FromContext ok=false, want true")
	}
	if got != &l {
		t.Fatalf("FromContext logger ptr mismatch: got %p want %p", got, &l)
	}
}

// WithContext(nil logger) → 원본 ctx 그대로.
func TestWithContext_NilLogger(t *testing.T) {
	t.Parallel()

	base := context.Background()
	got := WithContext(base, nil)
	if got != base {
		t.Fatalf("WithContext(nil logger) 가 원본 ctx 를 반환하지 않음")
	}
}

// REV-FINAL-002: WithContext(nil ctx, l) → context.Background() 로 대체 후 바인딩.
func TestWithContext_NilContextFallsBackToBackground(t *testing.T) {
	t.Parallel()

	l := zerolog.New(io.Discard)
	//nolint:staticcheck // 의도적으로 nil ctx 주입
	ctx := WithContext(nil, &l)
	if ctx == nil {
		t.Fatalf("WithContext(nil, l) 이 nil 반환")
	}
	got, ok := FromContext(ctx)
	if !ok || got != &l {
		t.Fatalf("nil ctx fallback 후 FromContext 실패: ok=%v got=%p want=%p", ok, got, &l)
	}
}

// FromContext(nil ctx) → (nil, false) (panic 없음).
func TestFromContext_NilContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // 의도적으로 nil ctx 주입
	got, ok := FromContext(nil)
	if ok || got != nil {
		t.Fatalf("FromContext(nil) ok=%v logger=%v, want (nil,false)", ok, got)
	}
}

// ctx 값에 로거가 아닌 다른 타입이 들어있으면 (nil, false).
func TestFromContext_WrongType(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), ctxKey{}, "not a logger")
	got, ok := FromContext(ctx)
	if ok || got != nil {
		t.Fatalf("타입 불일치 시 (nil,false) 기대, got ok=%v logger=%v", ok, got)
	}
}

// BenchmarkFromContext 는 성능 목표(< 100ns) 계측용이다.
func BenchmarkFromContext(b *testing.B) {
	l := zerolog.New(io.Discard)
	ctx := WithContext(context.Background(), &l)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = FromContext(ctx)
	}
}
