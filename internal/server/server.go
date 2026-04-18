// Package server 는 HTTP 리스너와 graceful shutdown 을 제공한다.
// 요청 ID/로깅/헬스체크/메트릭 같은 교차관심사는 본 패키지의 책임이 아니며
// 상위(미들웨어/핸들러) 에서 주입한다.
//
// Run 의 반환 에러 계약은 errors.go 의 sentinel 7종을 단일 소스로 한다.
// 값 적용 우선순위는 Option > config.Config > 내장 기본값이다(NFR-003 / NFR-004).
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
	"github.com/rs/zerolog"
)

// Server 는 설정 가능한 HTTP 리스너를 캡슐화한다.
// 모든 필드는 비공개이며 New 를 통해서만 초기화된다.
type Server struct {
	cfg    *config.Config
	logger *zerolog.Logger

	// 리스너는 Run 시점에 net.Listen 으로 생성되거나 WithListener 로 주입된다.
	injectedListener net.Listener

	// http.Server 에 주입되는 최종 값(Option > Config > Default 해석 후).
	resolvedReadHeaderTimeout time.Duration
	resolvedMaxHeaderBytes    int

	// 라우터 및 중복 패턴 검출용 집합(Handle 단계에서 사용).
	mux      *http.ServeMux
	patterns map[string]struct{}

	// 상태 머신 보호용. Run 이 한 번이라도 호출되었는지, 현재 바인딩된 주소.
	mu      sync.Mutex
	started bool
	addr    string
}

// New 는 설정과 로거로부터 Server 를 만든다.
// cfg 또는 logger 가 nil 이면 `server:` 접두 에러를 반환하고 panic 하지 않는다.
//
// Option 은 cfg 값을 덮어쓸 수 있으며 우선순위는
// Option > cfg.Config > 내장 기본값이다(NFR-003, NFR-004).
func New(cfg *config.Config, logger *zerolog.Logger, opts ...Option) (*Server, error) {
	if cfg == nil {
		return nil, errors.New("server: config is nil")
	}
	if logger == nil {
		return nil, errors.New("server: logger is nil")
	}

	var o options
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&o); err != nil {
			return nil, fmt.Errorf("server: option apply: %w", err)
		}
	}

	return &Server{
		cfg:                       cfg,
		logger:                    logger,
		injectedListener:          o.listener,
		resolvedReadHeaderTimeout: resolveReadHeaderTimeout(o.readHeaderTimeout, cfg.Server.ReadHeaderTimeout),
		resolvedMaxHeaderBytes:    resolveMaxHeaderBytes(o.maxHeaderBytes, cfg.Server.MaxHeaderBytes),
		mux:                       http.NewServeMux(),
		patterns:                  make(map[string]struct{}),
	}, nil
}

// Handle 은 path 패턴에 http.Handler 를 등록한다.
// panic-free 계약을 위해 다음 순서로 사전 검증한 뒤에만 mux.Handle 을 호출한다.
//
// 에러 (errors.Is 로 판별):
//   - Run 이 이미 호출됨     → ErrCannotRegisterAfterRun
//   - h == nil               → ErrInvalidHandler
//   - pattern 형식 불량      → ErrInvalidPattern
//   - 이미 등록된 pattern    → ErrDuplicatePattern (mux.Handle 은 호출되지 않음)
//
// mux.Handle 이 그럼에도 panic 을 던지면 recover 로 감싸 ErrInvalidPattern 으로 승격한다.
// panic 이 호출부로 전파되지 않음이 본 메서드의 불변식이다.
func (s *Server) Handle(pattern string, h http.Handler) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return ErrCannotRegisterAfterRun
	}
	if h == nil {
		return ErrInvalidHandler
	}
	if !isValidPattern(pattern) {
		return ErrInvalidPattern
	}
	if _, dup := s.patterns[pattern]; dup {
		return ErrDuplicatePattern
	}

	// http.ServeMux.Handle 이 panic 을 던지는 예외 경로(표준 라이브러리 버전 이슈 등)
	// 는 최후 방어선으로 recover 하여 에러로 승격한다.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: mux.Handle panic: %v", ErrInvalidPattern, r)
		}
	}()

	s.mux.Handle(pattern, h)
	s.patterns[pattern] = struct{}{}
	return nil
}

// isValidPattern 은 pattern 이 http.ServeMux 가 허용하는 path 형식인지 확인한다.
// 최소 스펙으로 비어있지 않고 `/` 로 시작해야 한다.
func isValidPattern(p string) bool {
	return p != "" && p[0] == '/'
}

// Addr 은 실제 바인딩된 주소(예: 127.0.0.1:34567) 를 반환한다.
// Run 호출 이전이면 빈 문자열을 반환한다.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addr
}

// Run 은 리스너를 가동하고 ctx.Done 까지 블로킹한다.
// ctx 취소 시 cfg.Server.GracefulShutdownTimeout 내에 graceful shutdown 을 수행한다.
//
// 반환 에러 계약 (FR-010 단일 소스):
//   - nil                : ctx 정상 취소 + shutdown 완료 (context.Canceled 는 nil 로 흡수)
//   - ErrServeFailed     : 리스너/Accept 등 Serve 조기 실패를 %w 래핑
//   - ErrShutdownTimeout : graceful shutdown 데드라인 초과 (context.DeadlineExceeded 를 %w 래핑)
//   - ErrAlreadyRunning  : Run 중복 호출 (프로그래밍 오류)
//
// ctx 가 nil 이면 panic 대신 `server: context is nil` 에러를 반환한다(panic-free 원칙).
// Run 내부에서 signal 패키지를 호출하지 않는다(FR-007). 시그널 처리는 호출자(main) 책임.
func (s *Server) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("server: context is nil")
	}

	// 1. 상태 머신: 중복 Run 검사 + 리스너 확보 + addr 세팅
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return ErrAlreadyRunning
	}

	listener, err := s.prepareListener()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %w", ErrServeFailed, err)
	}

	s.started = true
	s.addr = listener.Addr().String()
	s.mu.Unlock()

	// 2. http.Server 구성 (설정 + 해석된 보안 한계값 주입)
	httpSrv := &http.Server{
		Handler:           s.mux,
		ReadTimeout:       s.cfg.Server.ReadTimeout,
		WriteTimeout:      s.cfg.Server.WriteTimeout,
		IdleTimeout:       s.cfg.Server.IdleTimeout,
		ReadHeaderTimeout: s.resolvedReadHeaderTimeout,
		MaxHeaderBytes:    s.resolvedMaxHeaderBytes,
	}

	// 3. Serve 고루틴 — 종료 조건은 http.Server.Shutdown 또는 리스너 에러
	serveErrCh := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
			return
		}
		serveErrCh <- nil
	}()

	// 4. 대기: ctx 취소 → graceful shutdown, 또는 Serve 조기 실패
	select {
	case <-ctx.Done():
		return s.gracefulShutdown(httpSrv, serveErrCh)
	case err := <-serveErrCh:
		if err != nil {
			return fmt.Errorf("%w: %w", ErrServeFailed, err)
		}
		return nil
	}
}

// gracefulShutdown 은 httpSrv 를 데드라인 내에 종료시키고 결과 에러를 반환한다.
//
// 방어 설계 배경 (Adversarial Review critical 지적 대응):
//   - net/http 의 Shutdown 은 내부적으로 listenerGroup.Wait() 로 Serve 고루틴
//     복귀를 ctx 무시하고 대기한다. 주입된 리스너가 Accept 를 해제하지 못하면
//     Shutdown 자체가 무기한 블록될 수 있으므로, Shutdown 은 별도 고루틴에서
//     실행하고 bounded select 로 데드라인을 강제한다.
//   - httpSrv.Close() 역시 Go 1.22+ 에서 listenerGroup.Wait() 를 호출하므로
//     동일한 hang 위험이 있다. 데드라인 초과 경로에서 Close 도 fire-and-forget
//     고루틴으로 띄워 Run 이 block 되지 않도록 한다.
//   - serveErrCh 대기 또한 bounded select 로 제한한다. 주입 리스너가 끝내
//     복귀를 거부하면 해당 고루틴은 leak 되지만 Run 은 반환을 보장한다.
func (s *Server) gracefulShutdown(httpSrv *http.Server, serveErrCh <-chan error) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.Server.GracefulShutdownTimeout)
	defer cancel()

	shutdownDoneCh := make(chan error, 1)
	go func() {
		shutdownDoneCh <- httpSrv.Shutdown(shutdownCtx)
	}()

	var shutdownErr error
	select {
	case shutdownErr = <-shutdownDoneCh:
	case <-shutdownCtx.Done():
		shutdownErr = shutdownCtx.Err()
		// Close 도 listenerGroup.Wait() 에서 block 가능하므로 fire-and-forget.
		// Close 자체의 에러는 진단 목적으로 로그에 남긴다(AGENTS.md: 에러 무시 금지).
		go func() {
			if err := httpSrv.Close(); err != nil {
				s.logger.Warn().
					Err(err).
					Str("component", "server").
					Msg("force close failed after shutdown deadline")
			}
		}()
	}

	// Serve 고루틴이 ErrServerClosed 로 복귀할 시간을 주되 동일 데드라인(shutdownCtx) 을
	// 재사용한다. 별도 timer 로 GracefulShutdownTimeout 을 다시 기다리면 실제 Run 반환
	// 시점이 최대 2배가 되어 FR-005 의 "데드라인" 의미를 초과하기 때문이다.
	select {
	case <-serveErrCh:
	case <-shutdownCtx.Done():
	}

	if shutdownErr != nil {
		if errors.Is(shutdownErr, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrShutdownTimeout, shutdownErr)
		}
		return fmt.Errorf("%w: %w", ErrServeFailed, shutdownErr)
	}
	return nil
}

// prepareListener 는 Option 으로 주입된 리스너가 있으면 그것을 반환하고,
// 없으면 cfg.Server.Host:Port 에 TCP 리스너를 연다.
func (s *Server) prepareListener() (net.Listener, error) {
	if s.injectedListener != nil {
		return s.injectedListener, nil
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	return net.Listen("tcp", addr)
}

// readHeaderTimeout 은 테스트에서 우선순위를 검증하기 위한 내부 접근자다.
func (s *Server) readHeaderTimeout() time.Duration {
	return s.resolvedReadHeaderTimeout
}

// maxHeaderBytes 는 테스트에서 우선순위를 검증하기 위한 내부 접근자다.
func (s *Server) maxHeaderBytes() int {
	return s.resolvedMaxHeaderBytes
}

// resolveReadHeaderTimeout 은 Option > Config > Default 우선순위에 따라 값을 결정한다.
// Option 은 포인터로 주입 여부를 구분하고, Config 는 > 0 일 때만 채택한다.
func resolveReadHeaderTimeout(optVal *time.Duration, cfgVal time.Duration) time.Duration {
	if optVal != nil {
		return *optVal
	}
	if cfgVal > 0 {
		return cfgVal
	}
	return defaultReadHeaderTimeout
}

// resolveMaxHeaderBytes 은 Option > Config > Default 우선순위에 따라 값을 결정한다.
func resolveMaxHeaderBytes(optVal *int, cfgVal int) int {
	if optVal != nil {
		return *optVal
	}
	if cfgVal > 0 {
		return cfgVal
	}
	return defaultMaxHeaderBytes
}
