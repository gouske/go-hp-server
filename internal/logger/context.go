package logger

import (
	"context"

	"github.com/rs/zerolog"
)

// ctxKey 는 context 에 로거 포인터를 보관할 때 사용하는 비공개 키 타입이다.
// 외부 패키지에서 동일 키를 재구성할 수 없도록 unexported 하며,
// 서로 다른 패키지가 같은 키 값을 사용하지 못하게 빈 구조체로 유지한다.
type ctxKey struct{}

// WithContext 는 주어진 zerolog 로거를 ctx 에 바인딩한 새로운 ctx 를 반환한다.
//
// 동작 규칙:
//   - logger == nil 이면 원본 ctx 를 그대로 반환한다 (no-op).
//   - ctx == nil 이면 context.Background() 로 대체한 뒤 바인딩한다
//     (panic-free 원칙, REV-FINAL-002).
func WithContext(ctx context.Context, logger *zerolog.Logger) context.Context {
	if logger == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext 는 ctx 에 바인딩된 로거를 반환한다.
// 바인딩이 없으면 (nil, false) 를 반환하며, 본 패키지는 전역/기본 로거 폴백을 제공하지 않는다.
// 호출부는 명시적으로 주입된 로거를 전달받아 사용해야 한다.
func FromContext(ctx context.Context) (*zerolog.Logger, bool) {
	if ctx == nil {
		return nil, false
	}
	v := ctx.Value(ctxKey{})
	if v == nil {
		return nil, false
	}
	l, ok := v.(*zerolog.Logger)
	if !ok || l == nil {
		return nil, false
	}
	return l, true
}
