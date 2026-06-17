package reload

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
)

// renderYAMLSrc 는 reload.sources 만 바꾼 유효 YAML 을 만든다(나머지는 기본값).
func renderYAMLSrc(sources string) string {
	return fmt.Sprintf(`
server:
  host: "127.0.0.1"
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  graceful_shutdown_timeout: 30s
  read_header_timeout: 5s
  max_header_bytes: 1048576
  request_timeout: 30s
worker_pool:
  size: 100
  queue_size: 10000
reload:
  sources: %s
  debounce_ms: 200
log:
  level: "info"
  format: "json"
`, sources)
}

// TestReloader_StopBeforeStartThenStart 는 MAJOR-1: Stop 을 Start 전에 호출해도 상태가
// 오염되지 않고 이후 Register/Start/Trigger 가 정상 동작하는지 검증한다.
func TestReloader_StopBeforeStartThenStart(t *testing.T) {
	t.Parallel()
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0})
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}

	// Start 전 Stop 은 진짜 no-op 이어야 한다.
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop before start: %v", err)
	}

	a := &recSub{name: "a"}
	if err := r.Register(a); err != nil {
		t.Fatalf("Register after stop-before-start: %v (상태 오염 의심)", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start after stop-before-start: %v (상태 오염 의심)", err)
	}
	defer r.Stop()

	if err := r.Trigger("x"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForCount(t, a.count, 1, 2*time.Second)
}

// TestReloader_TriggerAfterStop 는 MAJOR-2: Stop 이후 Trigger 가 silent drop 대신
// ErrStopped 를 반환하는지 검증한다(FR-060b1 에 따라 ErrNotStarted 와 구분).
func TestReloader_TriggerAfterStop(t *testing.T) {
	t.Parallel()
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0})
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	err = r.Trigger("x")
	if !errors.Is(err, ErrStopped) {
		t.Errorf("Trigger after Stop = %v, want ErrStopped", err)
	}
	if errors.Is(err, ErrNotStarted) {
		t.Error("Trigger after Stop 은 ErrNotStarted 가 아니어야 함(FR-060b1)")
	}
}

// TestReloader_TriggerAfterCtxCancel 는 MAJOR-3: ctx 취소 자동 Stop 경로에서도
// stopped 가 전파되어 이후 Trigger 가 (명시적 Stop 없이도) ErrStopped 를 반환하는지 검증한다.
// ctx 취소 자동 Stop 은 비동기라 deadline polling 으로 stopped 전파를 기다린다.
func TestReloader_TriggerAfterCtxCancel(t *testing.T) {
	t.Parallel()
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0})
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	cancel() // ctx 취소 → 자동 Stop 경로 (명시적 Stop 호출 안 함)

	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = r.Trigger("x")
		if errors.Is(lastErr, ErrStopped) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !errors.Is(lastErr, ErrStopped) {
		t.Errorf("ctx 취소 후 Trigger = %v, want ErrStopped (silent drop 방지)", lastErr)
	}
}

// TestReloader_SourcesReorderNoWarn 은 MINOR-2: reload.sources 재정렬만으로는
// restart-required warning 이 발생하지 않는지(집합 비교) 검증한다.
func TestReloader_SourcesReorderNoWarn(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, renderYAMLSrc(`["sighup", "file"]`))
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0}, WithErrorLogger(newTestLogger(buf)))
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	a := &recSub{name: "a"}
	if err := r.Register(a); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	// 순서만 뒤집고 trigger → 집합이 동일하므로 warning 0건이어야 한다.
	writeYAMLFile(t, path, renderYAMLSrc(`["file", "sighup"]`))
	if err := r.Trigger("x"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForMessages(t, buf, summaryMsg, 1, 2*time.Second)

	warns := linesWithMessage(buf, "restart-required config fields changed; ignored until restart")
	if len(warns) != 0 {
		t.Errorf("reload.sources 재정렬만으로 warning %d건 발생, want 0\nlog:\n%s", len(warns), buf.String())
	}
}
