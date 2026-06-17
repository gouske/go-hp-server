package reload

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
)

// TestReloaderOptions_NilRejected 는 WithErrorLogger(nil)/WithClock(nil) 거부를 본다.
func TestReloaderOptions_NilRejected(t *testing.T) {
	t.Parallel()
	path := newConfigFile(t, defaultYAML())
	if _, err := NewReloader(path, config.ReloadConfig{}, WithErrorLogger(nil)); err == nil {
		t.Error("WithErrorLogger(nil): err = nil, want error")
	}
	if _, err := NewReloader(path, config.ReloadConfig{}, WithClock(nil)); err == nil {
		t.Error("WithClock(nil): err = nil, want error")
	}
}

// allChangedYAML 은 default 대비 재시작 필요 필드 10개가 모두 다른 유효 YAML 이다.
// hot-reload 필드(log.level/graceful/request_timeout)는 동일하게 유지한다.
const allChangedYAML = `
server:
  host: "0.0.0.0"
  port: 9090
  read_timeout: 25s
  write_timeout: 25s
  idle_timeout: 100s
  graceful_shutdown_timeout: 30s
  read_header_timeout: 3s
  max_header_bytes: 2097152
  request_timeout: 30s
worker_pool:
  size: 100
  queue_size: 10000
reload:
  sources: ["sighup", "file"]
  debounce_ms: 100
log:
  level: "info"
  format: "console"
`

// TestReloader_AllRestartFieldsWarnOnce 는 재시작 필요 필드 10개가 모두 변경돼도
// warning 이 1건으로 모이고 changed_fields 가 10개를 담는지 검증한다(warnRestartRequired 커버리지).
func TestReloader_AllRestartFieldsWarnOnce(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML())
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

	writeYAMLFile(t, path, allChangedYAML)
	if err := r.Trigger("all"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForMessages(t, buf, summaryMsg, 1, 2*time.Second)

	warns := linesWithMessage(buf, "restart-required config fields changed; ignored until restart")
	if len(warns) != 1 {
		t.Fatalf("warning count = %d, want 1", len(warns))
	}
	if got := toStrings(warns[0]["changed_fields"]); len(got) != 10 {
		t.Errorf("changed_fields len = %d (%v), want 10", len(got), got)
	}
	// reload 자체는 성공(hot 필드만 반영)해야 한다.
	if a.count() != 1 {
		t.Errorf("apply count = %d, want 1 (warning 은 reload 를 실패시키지 않음)", a.count())
	}
}

// TestReloader_LoadFailure 는 reload 시 config.Load 실패(타입 불일치) 경로를 검증한다.
func TestReloader_LoadFailure(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML())
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

	// port 를 문자열로 바꿔 Load(unmarshal) 단계에서 실패시킨다.
	writeYAMLFile(t, path, strings.Replace(defaultYAML(), "port: 8080", `port: "abc"`, 1))
	if err := r.Trigger("bad"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForMessages(t, buf, "config reload load failed", 1, 2*time.Second)

	if a.count() != 0 {
		t.Errorf("apply count = %d, want 0 (load 실패 시 미호출)", a.count())
	}
}
