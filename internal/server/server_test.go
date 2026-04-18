// Package server 의 테스트는 FEATURE_SPEC.md 의 P0-2 (FR-001~011, NFR-001~006) 시나리오를
// 근거로 작성한다. 본 파일은 골격(New, Addr, sentinel 에러) 범위만 커버하며,
// Handle / Run / graceful shutdown 은 후속 TDD 사이클에서 추가한다.
package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
	"github.com/rs/zerolog"
)

// okHandler 는 Handle 테스트에서 유효한 핸들러 인스턴스를 반환한다.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// mustNew 는 New 실패 시 t.Fatalf 로 테스트를 중단시키는 생성 헬퍼다.
// AGENTS.md 의 "에러 무시 금지" 규약을 테스트 코드에서 강제한다.
func mustNew(t *testing.T, cfg *config.Config, logger *zerolog.Logger, opts ...Option) *Server {
	t.Helper()
	srv, err := New(cfg, logger, opts...)
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	return srv
}

// testHTTPClient 는 짧은 타임아웃을 가진 http.Client 를 반환한다.
// 테스트가 예기치 못한 이유로 블록되지 않도록 상한을 명시한다.
func testHTTPClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second}
}

// newTestConfig 는 Validate 를 이미 통과한 상태의 최소 Config 를 반환한다.
// 각 테스트 케이스는 필요한 필드만 mutate 한다.
func newTestConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Host:                    "127.0.0.1",
			Port:                    0, // 바인딩은 WithListener 로 우회
			ReadTimeout:             30 * time.Second,
			WriteTimeout:            30 * time.Second,
			IdleTimeout:             120 * time.Second,
			GracefulShutdownTimeout: 30 * time.Second,
			ReadHeaderTimeout:       5 * time.Second,
			MaxHeaderBytes:          1 << 20,
		},
		Log: config.LogConfig{Level: "info", Format: "json"},
	}
}

// newTestLogger 는 출력을 버리는 zerolog.Logger 를 반환한다. 테스트 stdout 을 더럽히지 않는다.
func newTestLogger() *zerolog.Logger {
	l := zerolog.New(io.Discard)
	return &l
}

// TestNew_NilGuards 는 FR-001 의 nil 방어를 검증한다.
func TestNew_NilGuards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     *config.Config
		logger  *zerolog.Logger
		wantSub string
	}{
		{"nil config", nil, newTestLogger(), "config is nil"},
		{"nil logger", newTestConfig(), nil, "logger is nil"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, err := New(tc.cfg, tc.logger)
			if err == nil {
				t.Fatalf("New() err = nil, want error containing %q", tc.wantSub)
			}
			if srv != nil {
				t.Errorf("New() srv != nil on error path")
			}
			if !strings.HasPrefix(err.Error(), "server:") {
				t.Errorf("err = %q, want 'server:' prefix", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestNew_Succeeds 는 정상 입력 시 Server 가 반환되고
// Addr 이 Run 호출 이전에는 빈 문자열인지 (FR-008) 를 검증한다.
func TestNew_Succeeds(t *testing.T) {
	t.Parallel()
	srv, err := New(newTestConfig(), newTestLogger())
	if err != nil {
		t.Fatalf("New() err = %v, want nil", err)
	}
	if srv == nil {
		t.Fatal("New() srv = nil, want non-nil")
	}
	if got := srv.Addr(); got != "" {
		t.Errorf("Addr() = %q before Run, want empty string", got)
	}
}

// TestSentinelErrors 는 공개 sentinel 에러 7종이 모두 정의되어 있고
// 메시지가 `server:` 접두로 시작하는지 확인한다. (FEATURE_SPEC FR-010 단일 소스)
func TestSentinelErrors(t *testing.T) {
	t.Parallel()
	sentinels := map[string]error{
		"ErrServeFailed":            ErrServeFailed,
		"ErrShutdownTimeout":        ErrShutdownTimeout,
		"ErrAlreadyRunning":         ErrAlreadyRunning,
		"ErrCannotRegisterAfterRun": ErrCannotRegisterAfterRun,
		"ErrDuplicatePattern":       ErrDuplicatePattern,
		"ErrInvalidHandler":         ErrInvalidHandler,
		"ErrInvalidPattern":         ErrInvalidPattern,
	}
	for name, err := range sentinels {
		name, err := name, err
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err == nil {
				t.Fatalf("%s is nil — sentinel must be initialized", name)
			}
			if !strings.HasPrefix(err.Error(), "server:") {
				t.Errorf("%s.Error() = %q, want 'server:' prefix", name, err.Error())
			}
			// errors.Is 는 동일 인스턴스에 대해 true 여야 한다 (sanity check).
			if !errors.Is(err, err) {
				t.Errorf("errors.Is(%s, %s) = false, want true", name, name)
			}
		})
	}
}

// TestOptionPriority 는 NFR-003/NFR-004 의 값 결정 우선순위 계약을 검증한다:
//
//	Option > config.Config > 내장 기본값
//
// 내부 필드 검사를 위해 같은 패키지에서 접근한다.
func TestOptionPriority(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig()
	cfg.Server.ReadHeaderTimeout = 7 * time.Second
	cfg.Server.MaxHeaderBytes = 2 << 20

	// Option 이 Config 값을 덮어쓴다.
	srv, err := New(cfg, newTestLogger(),
		WithReadHeaderTimeout(11*time.Second),
		WithMaxHeaderBytes(4<<20),
	)
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	if got := srv.readHeaderTimeout(); got != 11*time.Second {
		t.Errorf("readHeaderTimeout = %s, want 11s (Option wins)", got)
	}
	if got := srv.maxHeaderBytes(); got != 4<<20 {
		t.Errorf("maxHeaderBytes = %d, want 4MiB (Option wins)", got)
	}

	// Option 없이 → Config 값 사용.
	srv2, err := New(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	if got := srv2.readHeaderTimeout(); got != 7*time.Second {
		t.Errorf("readHeaderTimeout = %s, want 7s (Config)", got)
	}
	if got := srv2.maxHeaderBytes(); got != 2<<20 {
		t.Errorf("maxHeaderBytes = %d, want 2MiB (Config)", got)
	}
}

// TestHandle 은 FR-003 의 사전 검증 3단계 + FR-004 의 상태 머신을 검증한다.
// 모든 실패 경로는 panic 없이 sentinel 에러를 반환해야 한다(panic-free 계약).
func TestHandle(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		srv := mustNew(t, newTestConfig(), newTestLogger())
		if err := srv.Handle("/ping", okHandler()); err != nil {
			t.Fatalf("Handle() err = %v, want nil", err)
		}
		// 같은 Server 에 다른 pattern 등록도 정상.
		if err := srv.Handle("/health", okHandler()); err != nil {
			t.Fatalf("Handle() 두 번째 err = %v, want nil", err)
		}
	})

	t.Run("nil handler → ErrInvalidHandler", func(t *testing.T) {
		t.Parallel()
		srv := mustNew(t, newTestConfig(), newTestLogger())
		err := srv.Handle("/x", nil)
		if !errors.Is(err, ErrInvalidHandler) {
			t.Fatalf("err = %v, want errors.Is(err, ErrInvalidHandler)", err)
		}
	})

	t.Run("empty pattern → ErrInvalidPattern", func(t *testing.T) {
		t.Parallel()
		srv := mustNew(t, newTestConfig(), newTestLogger())
		err := srv.Handle("", okHandler())
		if !errors.Is(err, ErrInvalidPattern) {
			t.Fatalf("err = %v, want errors.Is(err, ErrInvalidPattern)", err)
		}
	})

	t.Run("no leading slash → ErrInvalidPattern", func(t *testing.T) {
		t.Parallel()
		srv := mustNew(t, newTestConfig(), newTestLogger())
		err := srv.Handle("no-slash", okHandler())
		if !errors.Is(err, ErrInvalidPattern) {
			t.Fatalf("err = %v, want errors.Is(err, ErrInvalidPattern)", err)
		}
	})

	t.Run("duplicate pattern → ErrDuplicatePattern", func(t *testing.T) {
		t.Parallel()
		srv := mustNew(t, newTestConfig(), newTestLogger())
		if err := srv.Handle("/dup", okHandler()); err != nil {
			t.Fatalf("첫 번째 Handle err = %v", err)
		}
		err := srv.Handle("/dup", okHandler())
		if !errors.Is(err, ErrDuplicatePattern) {
			t.Fatalf("err = %v, want errors.Is(err, ErrDuplicatePattern)", err)
		}
	})

	t.Run("after run → ErrCannotRegisterAfterRun", func(t *testing.T) {
		t.Parallel()
		srv := mustNew(t, newTestConfig(), newTestLogger())
		// Run 구현 없이 상태 머신을 검증하기 위해 직접 세팅한다.
		// (Run 통합 테스트는 TestRun_* 에서 실제 Run 으로 재검증한다).
		srv.mu.Lock()
		srv.started = true
		srv.mu.Unlock()

		err := srv.Handle("/late", okHandler())
		if !errors.Is(err, ErrCannotRegisterAfterRun) {
			t.Fatalf("err = %v, want errors.Is(err, ErrCannotRegisterAfterRun)", err)
		}
	})
}

// newLocalListener 는 127.0.0.1:0 에 바인딩된 실제 TCP 리스너를 반환한다.
// OS 가 임의 포트를 할당하므로 테스트 간 포트 충돌이 없다.
func newLocalListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	return ln
}

// TestRun_NormalShutdown 은 FR-002, FR-008, FR-010(nil) 을 검증한다:
// 정상 기동 → 요청 처리 → ctx cancel → graceful shutdown → Run 이 nil 반환.
func TestRun_NormalShutdown(t *testing.T) {
	t.Parallel()
	ln := newLocalListener(t)
	srv, err := New(newTestConfig(), newTestLogger(), WithListener(ln))
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}
	if err := srv.Handle("/ping", okHandler()); err != nil {
		t.Fatalf("Handle() err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Serve 고루틴이 시작될 때까지 Addr 이 세팅되길 폴링
	deadline := time.Now().Add(500 * time.Millisecond)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if srv.Addr() == "" {
		t.Fatal("Addr() empty after Run 기동")
	}

	resp, err := testHTTPClient().Get("http://" + srv.Addr() + "/ping")
	if err != nil {
		t.Fatalf("http.Get err = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run err = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s after ctx cancel")
	}
}

// TestRun_AlreadyRunning 은 FR-010 의 ErrAlreadyRunning 분기를 검증한다.
func TestRun_AlreadyRunning(t *testing.T) {
	t.Parallel()
	ln := newLocalListener(t)
	srv := mustNew(t, newTestConfig(), newTestLogger(), WithListener(ln))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// 첫 번째 Run 이 started=true 를 세팅할 때까지 폴링
	deadline := time.Now().Add(500 * time.Millisecond)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	err := srv.Run(context.Background())
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("두 번째 Run err = %v, want ErrAlreadyRunning", err)
	}

	cancel()
	<-done
}

// TestRun_ShutdownTimeout 은 FR-005 / FR-010 의 ErrShutdownTimeout 경로를 검증한다.
// 블로킹 핸들러가 응답을 지연시키고, ShutdownTimeout 을 짧게 설정해 타임아웃을 유도한다.
func TestRun_ShutdownTimeout(t *testing.T) {
	t.Parallel()
	ln := newLocalListener(t)
	cfg := newTestConfig()
	cfg.Server.GracefulShutdownTimeout = 50 * time.Millisecond

	srv := mustNew(t, cfg, newTestLogger(), WithListener(ln))

	blockerStarted := make(chan struct{})
	if err := srv.Handle("/block", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(blockerStarted)
		time.Sleep(800 * time.Millisecond)
	})); err != nil {
		t.Fatalf("Handle() err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Addr 확보 후 백그라운드 요청으로 핸들러를 점유시킨다.
	for srv.Addr() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	go func() {
		resp, err := testHTTPClient().Get("http://" + srv.Addr() + "/block")
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-blockerStarted

	cancel() // shutdown 개시. 핸들러가 아직 블록 중이라 50ms 타임아웃 초과.

	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Errorf("Run err = %v, want errors.Is(err, ErrShutdownTimeout)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s")
	}
}

// TestRun_ServeFailsOnClosedListener 는 FR-006 / FR-010 의 ErrServeFailed 경로를 검증한다.
// 이미 닫힌 리스너를 주입하면 Serve 가 즉시 실패한다.
func TestRun_ServeFailsOnClosedListener(t *testing.T) {
	t.Parallel()
	ln := newLocalListener(t)
	ln.Close()

	srv := mustNew(t, newTestConfig(), newTestLogger(), WithListener(ln))

	err := srv.Run(context.Background())
	if !errors.Is(err, ErrServeFailed) {
		t.Errorf("Run err = %v, want errors.Is(err, ErrServeFailed)", err)
	}
}

// TestRun_NilContext 는 ctx == nil 입력이 panic 대신 server 접두 에러를
// 반환하는지 검증한다(AGENTS.md panic 금지 규칙).
func TestRun_NilContext(t *testing.T) {
	t.Parallel()
	ln := newLocalListener(t)
	srv := mustNew(t, newTestConfig(), newTestLogger(), WithListener(ln))

	//nolint:staticcheck // 의도적으로 nil context 를 전달해 방어 경로를 검증한다.
	err := srv.Run(nil)
	if err == nil {
		t.Fatal("Run(nil) err = nil, want 'server: context is nil'")
	}
	if !strings.HasPrefix(err.Error(), "server:") {
		t.Errorf("err = %q, want 'server:' prefix", err.Error())
	}
	if !strings.Contains(err.Error(), "context is nil") {
		t.Errorf("err = %q, want substring 'context is nil'", err.Error())
	}
	// 리스너 정리
	if err := ln.Close(); err != nil {
		t.Logf("ln.Close: %v (non-fatal)", err)
	}
}

// TestRun_ListenFailsOnInvalidHost 는 WithListener 없이 호출될 때
// net.Listen 실패 경로가 ErrServeFailed 로 래핑되는지 검증한다.
func TestRun_ListenFailsOnInvalidHost(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig()
	cfg.Server.Host = "invalid-host-nonexistent-999.invalid"
	cfg.Server.Port = 0

	srv := mustNew(t, cfg, newTestLogger())

	err := srv.Run(context.Background())
	if !errors.Is(err, ErrServeFailed) {
		t.Errorf("Run err = %v, want errors.Is(err, ErrServeFailed)", err)
	}
}

// TestOption_RejectsNonPositive 는 보안 한계값 옵션이 0/음수 입력을 즉시 거부하는지 검증한다.
// (Adversarial Review 지적 사항: Option 우선순위가 Config.Validate 를 우회하지 못하게 한다.)
func TestOption_RejectsNonPositive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opt  Option
	}{
		{"WithReadHeaderTimeout zero", WithReadHeaderTimeout(0)},
		{"WithReadHeaderTimeout negative", WithReadHeaderTimeout(-1 * time.Second)},
		{"WithMaxHeaderBytes zero", WithMaxHeaderBytes(0)},
		{"WithMaxHeaderBytes negative", WithMaxHeaderBytes(-1)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, err := New(newTestConfig(), newTestLogger(), tc.opt)
			if err == nil {
				t.Fatalf("New() err = nil, want non-positive rejection")
			}
			if srv != nil {
				t.Errorf("srv != nil on error path")
			}
			if !strings.Contains(err.Error(), "must be > 0") {
				t.Errorf("err = %q, want substring 'must be > 0'", err.Error())
			}
		})
	}
}

// stuckListener 는 Close 가 호출돼도 Accept 가 풀리지 않는 리스너를 시뮬레이트한다.
// 실서비스에서는 일어나기 어렵지만, 주입된 커스텀 리스너가 Close 보장을 약속하지 못할 때
// Run 의 shutdown 경로가 hang 되는지 검증하기 위한 adversarial test double 이다.
type stuckListener struct {
	closeCalled chan struct{}
}

func newStuckListener() *stuckListener {
	return &stuckListener{closeCalled: make(chan struct{})}
}

func (l *stuckListener) Accept() (net.Conn, error) {
	// Close 이후에도 blocked 상태를 유지해 Serve 고루틴이 복귀하지 않는 상황을 만든다.
	<-l.closeCalled
	// closeCalled 가 닫혀도 여전히 반환을 지연시켜 hang 을 재현한다.
	time.Sleep(10 * time.Second)
	return nil, net.ErrClosed
}

func (l *stuckListener) Close() error {
	select {
	case <-l.closeCalled: // 이미 닫힘
	default:
		close(l.closeCalled)
	}
	return nil
}

func (l *stuckListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
}

// TestRun_ShutdownHangProtection 은 Serve 고루틴이 응답하지 않는 극단 케이스에서도
// Run 이 ErrShutdownTimeout 으로 bounded 내에 반환하는지 검증한다.
// (Adversarial Review critical 지적 회귀 방지 테스트.)
func TestRun_ShutdownHangProtection(t *testing.T) {
	t.Parallel()
	cfg := newTestConfig()
	cfg.Server.GracefulShutdownTimeout = 50 * time.Millisecond

	srv, err := New(cfg, newTestLogger(), WithListener(newStuckListener()))
	if err != nil {
		t.Fatalf("New() err = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// 서버 기동 대기
	deadline := time.Now().Add(500 * time.Millisecond)
	for srv.Addr() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Errorf("Run err = %v, want errors.Is(err, ErrShutdownTimeout)", err)
		}
	case <-time.After(2 * time.Second):
		// 실패 시 진단용 goroutine 덤프 (hang 재발 시 원인 추적 용이성)
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Logf("=== goroutine dump on hang ===\n%s", buf[:n])
		t.Fatal("Run did not return within 2s despite stuck listener — hang protection broken")
	}
}
