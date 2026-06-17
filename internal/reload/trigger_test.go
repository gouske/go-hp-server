package reload

import (
	"context"
	"testing"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
)

// TestFileTrigger_DetectsChange 는 TS-78: 파일 watch 활성 시 YAML 수정이 reload 를
// 유발하는지 deadline polling 으로 검증한다(고정 sleep 미사용, REV6-MINOR-003).
func TestFileTrigger_DetectsChange(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{Sources: []string{"file"}, DebounceMs: 20},
		WithErrorLogger(newTestLogger(buf)))
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

	// 파일 변경(log.level info→debug) → fsnotify 이벤트 → debounce 후 Subscriber 호출.
	writeYAMLFile(t, path, renderYAML("debug", "json", 8080, "30s", "30s"))

	waitForCount(t, a.count, 1, 3*time.Second)
	if got := a.lastCfg().Log.Level; got != "debug" {
		t.Errorf("reload 후 log.level = %q, want debug", got)
	}
}
