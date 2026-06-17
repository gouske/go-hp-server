//go:build !windows

package reload

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/gouske/go-hp-server/internal/config"
)

// TestSighupTrigger_DetectsSignal 은 TS-77: SIGHUP 수신이 reload 를 유발하는지 검증한다.
//
// 테스트 격리(REV6-MINOR-004): 프로세스 전체 시그널 상태에 의존하므로 t.Parallel 을
// 사용하지 않는다. Start 가 signal.Notify 를 동기 등록한 뒤 SIGHUP 을 보내므로 기본 종료
// 동작으로 프로세스가 죽지 않는다.
func TestSighupTrigger_DetectsSignal(t *testing.T) {
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{Sources: []string{"sighup"}, DebounceMs: 0},
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

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	waitForCount(t, a.count, 1, 3*time.Second)
}
