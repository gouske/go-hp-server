package server

import (
	"fmt"
	"net"
	"time"
)

// 내장 기본값. Option / Config 모두 값을 제공하지 않을 때 사용된다.
// 명세 NFR-003 / NFR-004 에 따라 slowloris · 헤더 폭탄 방어를 위해 반드시 명시돼야 한다.
const (
	defaultReadHeaderTimeout = 5 * time.Second
	defaultMaxHeaderBytes    = 1 << 20 // 1 MiB
)

// options 는 New 시점에 수집되는 내부 설정이다. 외부에 노출되지 않는다.
// 각 포인터 필드는 "Option 이 명시적으로 값을 주입했는지" 를 식별해
// Option > Config > Default 우선순위를 구현하는 데 사용된다.
type options struct {
	listener          net.Listener
	readHeaderTimeout *time.Duration
	maxHeaderBytes    *int
}

// Option 은 Server 생성 시점에 내부 설정을 바꾸는 함수형 옵션이다.
// 값 적용 우선순위는 모든 Option 에 대해 동일하다:
//
//	Option > config.Config > 내장 기본값
//
// 동일 Option 이 여러 번 전달되면 마지막 호출이 이긴다.
type Option func(*options) error

// WithListener 는 외부에서 만든 net.Listener 를 주입한다.
// 테스트에서 net.Listen("tcp", "127.0.0.1:0") 으로 임의 포트를 확보할 때 사용한다.
func WithListener(l net.Listener) Option {
	return func(o *options) error {
		o.listener = l
		return nil
	}
}

// WithReadHeaderTimeout 은 slowloris 방어용 헤더 읽기 타임아웃을 재정의한다.
// Option 이 주입되면 cfg.Server.ReadHeaderTimeout 보다 우선한다.
//
// 보안 한계값이므로 d 는 반드시 양수여야 한다. 0 또는 음수가 주입되면
// Config.Validate 를 우회해 slowloris 방어가 무력화되는 것을 방지하기 위해
// 옵션 적용 단계에서 즉시 에러를 반환한다.
func WithReadHeaderTimeout(d time.Duration) Option {
	return func(o *options) error {
		if d <= 0 {
			return fmt.Errorf("WithReadHeaderTimeout: must be > 0, got %s", d)
		}
		o.readHeaderTimeout = &d
		return nil
	}
}

// WithMaxHeaderBytes 는 헤더 폭탄/메모리 고갈 방어용 최대 헤더 바이트를 재정의한다.
// Option 이 주입되면 cfg.Server.MaxHeaderBytes 보다 우선한다.
//
// 보안 한계값이므로 n 은 반드시 양수여야 한다. 0 또는 음수가 주입되면
// Config.Validate 를 우회해 헤더 크기 방어가 무력화되는 것을 방지하기 위해
// 옵션 적용 단계에서 즉시 에러를 반환한다.
func WithMaxHeaderBytes(n int) Option {
	return func(o *options) error {
		if n <= 0 {
			return fmt.Errorf("WithMaxHeaderBytes: must be > 0, got %d", n)
		}
		o.maxHeaderBytes = &n
		return nil
	}
}
