package server

import (
	"context"
	"errors"

	"github.com/gouske/go-hp-server/internal/config"
)

// ServerSubscriber 는 Server 를 reload.Subscriber 로 래핑한다(P0-4).
//
// Apply 는 UpdateShutdownTimeout 만 호출한다. 재시작 필요 필드(host/port 등)의 변경
// warning 은 Reloader 의 공통 경로가 단독으로 수행하므로 여기서는 관여하지 않는다(REV5-004).
// 따라서 본 타입은 자체 로거를 갖지 않으며 어떤 warning 도 생성하지 않는다.
type ServerSubscriber struct {
	srv *Server
}

// NewServerSubscriber 는 Server 를 감싸는 ServerSubscriber 를 생성한다.
// s 가 nil 이면 `server:` 접두 에러를 반환하며 panic 하지 않는다.
func NewServerSubscriber(s *Server) (*ServerSubscriber, error) {
	if s == nil {
		return nil, errors.New("server: server subscriber requires non-nil server")
	}
	return &ServerSubscriber{srv: s}, nil
}

// Name 은 Subscriber 식별자 "graceful_shutdown_timeout" 을 반환한다.
func (ss *ServerSubscriber) Name() string {
	return "graceful_shutdown_timeout"
}

// Apply 는 cfg.Server.GracefulShutdownTimeout 을 Server 에 반영한다(reload.Subscriber).
// cfg 가 nil 이거나 값이 비양수면 에러를 반환하고 기존 값을 유지한다.
func (ss *ServerSubscriber) Apply(_ context.Context, cfg *config.Config) error {
	if cfg == nil {
		return errors.New("server: server subscriber apply: nil config")
	}
	return ss.srv.UpdateShutdownTimeout(cfg.Server.GracefulShutdownTimeout)
}
