// Package server 의 ServerSubscriber 및 UpdateShutdownTimeout 테스트는
// P0-4 의 TS-66 ~ TS-67 을 커버한다.
package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// TestServer_UpdateShutdownTimeout 은 atomic 갱신과 비양수 거부를 검증한다.
func TestServer_UpdateShutdownTimeout(t *testing.T) {
	t.Parallel()
	srv := mustNew(t, newTestConfig(), newTestLogger())

	if got := srv.shutdownTimeout(); got != 30*time.Second {
		t.Fatalf("초기 shutdownTimeout = %s, want 30s (cfg 에서 초기화)", got)
	}
	if err := srv.UpdateShutdownTimeout(200 * time.Millisecond); err != nil {
		t.Fatalf("UpdateShutdownTimeout() err = %v, want nil", err)
	}
	if got := srv.shutdownTimeout(); got != 200*time.Millisecond {
		t.Errorf("shutdownTimeout = %s, want 200ms", got)
	}

	for _, d := range []time.Duration{0, -1 * time.Second} {
		if err := srv.UpdateShutdownTimeout(d); err == nil {
			t.Errorf("UpdateShutdownTimeout(%s) err = nil, want error", d)
		}
	}
	// 거부된 갱신 후에도 직전 유효값이 유지되어야 한다.
	if got := srv.shutdownTimeout(); got != 200*time.Millisecond {
		t.Errorf("거부 후 shutdownTimeout = %s, want 200ms (불변)", got)
	}
}

// TestNewServerSubscriber_NilServer 는 nil Server 주입 시 에러를 반환하는지 본다.
func TestNewServerSubscriber_NilServer(t *testing.T) {
	t.Parallel()
	if _, err := NewServerSubscriber(nil); err == nil {
		t.Fatal("NewServerSubscriber(nil) err = nil, want error")
	}
}

// TestServerSubscriber_Apply 는 TS-66: Apply 가 graceful shutdown timeout 만 반영하는지 본다.
func TestServerSubscriber_Apply(t *testing.T) {
	t.Parallel()
	srv := mustNew(t, newTestConfig(), newTestLogger())
	sub, err := NewServerSubscriber(srv)
	if err != nil {
		t.Fatalf("NewServerSubscriber() err = %v", err)
	}
	if sub.Name() != "graceful_shutdown_timeout" {
		t.Errorf("Name() = %q, want %q", sub.Name(), "graceful_shutdown_timeout")
	}

	cfg := newTestConfig()
	cfg.Server.GracefulShutdownTimeout = 200 * time.Millisecond
	if err := sub.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply() err = %v, want nil", err)
	}
	if got := srv.shutdownTimeout(); got != 200*time.Millisecond {
		t.Errorf("Apply 후 shutdownTimeout = %s, want 200ms", got)
	}
}

// TestServerSubscriber_Apply_NoWarningOnRestartField 는 TS-67(REV5-004):
// 재시작 필요 필드(port 등)가 바뀐 cfg 를 Apply 해도 ServerSubscriber 는 warning 을
// 생성하지 않고(자체 로거 없음) shutdown timeout 만 반영하며 에러 없이 반환하는지 본다.
func TestServerSubscriber_Apply_NoWarningOnRestartField(t *testing.T) {
	t.Parallel()
	srv := mustNew(t, newTestConfig(), newTestLogger())
	sub, err := NewServerSubscriber(srv)
	if err != nil {
		t.Fatalf("NewServerSubscriber() err = %v", err)
	}

	cfg := newTestConfig()
	cfg.Server.Port = 9090 // 재시작 필요 필드 변경
	cfg.Server.GracefulShutdownTimeout = 150 * time.Millisecond
	if err := sub.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply() err = %v, want nil", err)
	}
	if got := srv.shutdownTimeout(); got != 150*time.Millisecond {
		t.Errorf("shutdownTimeout = %s, want 150ms (shutdown timeout 만 반영)", got)
	}
}

// TestRun_ShutdownTimeoutHonorsUpdate 는 TS-66 통합: UpdateShutdownTimeout 으로 설정한
// 짧은 deadline 이 cfg 의 긴 기본값 대신 실제 shutdown 에 사용되는지 검증한다.
func TestRun_ShutdownTimeoutHonorsUpdate(t *testing.T) {
	t.Parallel()
	ln := newLocalListener(t)
	cfg := newTestConfig()
	cfg.Server.GracefulShutdownTimeout = 30 * time.Second // 일부러 긴 기본값

	srv := mustNew(t, cfg, newTestLogger(), WithListener(ln))
	if err := srv.UpdateShutdownTimeout(50 * time.Millisecond); err != nil {
		t.Fatalf("UpdateShutdownTimeout() err = %v", err)
	}

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

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Errorf("Run err = %v, want errors.Is(err, ErrShutdownTimeout) (50ms 적용 증명)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s; UpdateShutdownTimeout 미적용 의심")
	}
}
