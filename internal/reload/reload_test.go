// Package reload 테스트는 P0-4 의 TS-68 ~ TS-76d 를 커버한다.
//
// 결정론적 검증을 위해 (a) 수동 Trigger + debounce=0(즉시 dispatch) 또는
// (b) WithClock 으로 주입한 fakeClock 을 사용하고, 비동기 reload 완료는
// deadline polling(REV6-MINOR-003) 으로 동기화한다.
package reload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
)

// --- 테스트 헬퍼 ---

// recSub 는 Apply 호출을 기록하는 Subscriber 이다. entered/release 가 설정되면
// Apply 가 진입 신호를 보내고 release 까지 블록해 dispatch 중 동시 trigger 를 재현한다.
type recSub struct {
	name     string
	failWith error
	entered  chan struct{}
	release  chan struct{}

	mu   sync.Mutex
	cfgs []*config.Config
}

func (s *recSub) Name() string { return s.name }

func (s *recSub) Apply(_ context.Context, cfg *config.Config) error {
	if s.entered != nil {
		s.entered <- struct{}{}
	}
	if s.release != nil {
		<-s.release
	}
	s.mu.Lock()
	s.cfgs = append(s.cfgs, cfg)
	s.mu.Unlock()
	return s.failWith
}

func (s *recSub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cfgs)
}

func (s *recSub) lastCfg() *config.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cfgs) == 0 {
		return nil
	}
	return s.cfgs[len(s.cfgs)-1]
}

// safeBuffer 는 동시 Write/Read 안전한 로그 버퍼이다.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeClock 는 debounce 타이머를 결정론적으로 제어한다.
type fakeClock struct {
	newCh chan *fakeTimer
}

func newFakeClock() *fakeClock { return &fakeClock{newCh: make(chan *fakeTimer, 8)} }

func (c *fakeClock) NewTimer(_ time.Duration) Timer {
	t := &fakeTimer{ch: make(chan time.Time, 1), resetCh: make(chan struct{}, 64)}
	c.newCh <- t
	return t
}

type fakeTimer struct {
	ch      chan time.Time
	resetCh chan struct{}
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }
func (t *fakeTimer) Stop() bool          { return true }
func (t *fakeTimer) Reset(_ time.Duration) {
	select {
	case t.resetCh <- struct{}{}:
	default:
	}
}
func (t *fakeTimer) fire() { t.ch <- time.Time{} }

// renderYAML 은 Validate 를 통과하는 전체 설정 YAML 을 생성한다.
func renderYAML(level, format string, port int, requestTimeout, graceful string) string {
	return fmt.Sprintf(`
server:
  host: "127.0.0.1"
  port: %d
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  graceful_shutdown_timeout: %s
  read_header_timeout: 5s
  max_header_bytes: 1048576
  request_timeout: %s
worker_pool:
  size: 100
  queue_size: 10000
reload:
  sources: ["sighup"]
  debounce_ms: 200
log:
  level: %q
  format: %q
`, port, graceful, requestTimeout, level, format)
}

func defaultYAML() string { return renderYAML("info", "json", 8080, "30s", "30s") }

// writeYAMLFile 은 path 에 content 를 기록한다(원자적 교체 아님, 직접 쓰기).
func writeYAMLFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
}

// newConfigFile 은 임시 디렉터리에 초기 YAML 을 작성하고 경로를 반환한다.
func newConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeYAMLFile(t, path, content)
	return path
}

func newTestLogger(buf *safeBuffer) *zerolog.Logger {
	l := zerolog.New(buf)
	return &l
}

func waitForCount(t *testing.T, fn func() int, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout: count = %d, want >= %d", fn(), want)
}

// parsedLines 는 버퍼의 JSON 로그 라인들을 맵으로 파싱한다.
func parsedLines(buf *safeBuffer) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			out = append(out, m)
		}
	}
	return out
}

func linesWithMessage(buf *safeBuffer, msg string) []map[string]any {
	var out []map[string]any
	for _, m := range parsedLines(buf) {
		if m["message"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func waitForMessages(t *testing.T, buf *safeBuffer, msg string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(linesWithMessage(buf, msg)) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout: %q count = %d, want >= %d\nlog:\n%s", msg, len(linesWithMessage(buf, msg)), want, buf.String())
}

func toStrings(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const summaryMsg = "config reload applied"

// --- 테스트 ---

// TestNewReloader_Validation 은 경로/소스 화이트리스트 검증을 본다.
func TestNewReloader_Validation(t *testing.T) {
	t.Parallel()
	path := newConfigFile(t, defaultYAML())

	if _, err := NewReloader("", config.ReloadConfig{}); err == nil {
		t.Error("empty path: err = nil, want error")
	}
	if _, err := NewReloader(filepath.Join(t.TempDir(), "missing.yaml"), config.ReloadConfig{}); err == nil {
		t.Error("missing path: err = nil, want error")
	}
	if _, err := NewReloader(path, config.ReloadConfig{Sources: []string{"bogus"}}); err == nil {
		t.Error("invalid source: err = nil, want ErrInvalidSource")
	}
	if r, err := NewReloader(path, config.ReloadConfig{Sources: []string{"sighup", "file"}}); err != nil || r == nil {
		t.Errorf("valid sources: r=%v err=%v, want non-nil reloader", r, err)
	}
}

// TestReloader_BasicDispatch 는 TS-68: Trigger 시 등록 순으로 Apply 되고 요약 로그 1건이
// succeeded 를 채우는지 검증한다.
func TestReloader_BasicDispatch(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{Sources: nil, DebounceMs: 0}, WithErrorLogger(newTestLogger(buf)))
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	a, b := &recSub{name: "a"}, &recSub{name: "b"}
	if err := r.Register(a); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := r.Register(b); err != nil {
		t.Fatalf("Register b: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	if err := r.Trigger("test"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}

	waitForMessages(t, buf, summaryMsg, 1, 2*time.Second)
	if a.count() != 1 || b.count() != 1 {
		t.Errorf("apply counts a=%d b=%d, want 1 each", a.count(), b.count())
	}
	sum := linesWithMessage(buf, summaryMsg)[0]
	if got := toStrings(sum["succeeded"]); !equalStrings(got, []string{"a", "b"}) {
		t.Errorf("succeeded = %v, want [a b]", got)
	}
	if got := toStrings(sum["failed"]); len(got) != 0 {
		t.Errorf("failed = %v, want empty", got)
	}
	if got := toStrings(sum["trigger_sources"]); !equalStrings(got, []string{"test"}) {
		t.Errorf("trigger_sources = %v, want [test]", got)
	}
	// 전체 성공 시 요약은 info 레벨이어야 한다.
	if sum["level"] != "info" {
		t.Errorf("level = %v, want info (전체 성공)", sum["level"])
	}
}

// TestReloader_ValidateFailRollback 은 TS-69: Validate 실패 시 Subscriber 미호출 +
// lastAppliedConfig 불변을 검증한다.
func TestReloader_ValidateFailRollback(t *testing.T) {
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

	// 1) 정상 reload → count 1
	if err := r.Trigger("ok1"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForCount(t, a.count, 1, 2*time.Second)
	firstApplied := r.lastApplied()
	if firstApplied == nil {
		t.Fatal("lastApplied = nil after successful reload")
	}

	// 2) YAML 을 무효(port 0)로 바꾸고 trigger → validate 실패, Subscriber 미호출
	writeYAMLFile(t, path, strings.Replace(defaultYAML(), "port: 8080", "port: 0", 1))
	if err := r.Trigger("bad"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForMessages(t, buf, "config reload validation failed", 1, 2*time.Second)

	if a.count() != 1 {
		t.Errorf("apply count = %d, want 1 (validate 실패 시 미호출)", a.count())
	}
	if r.lastApplied() != firstApplied {
		t.Error("lastApplied 가 validate 실패 후 변경됨, want 불변")
	}
}

// TestReloader_SubscriberFailureIsolation 은 TS-70: 한 Subscriber 실패해도 나머지는
// 호출되고, 부분 성공 시 lastAppliedConfig 가 갱신되는지 검증한다.
func TestReloader_SubscriberFailureIsolation(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0}, WithErrorLogger(newTestLogger(buf)))
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	a := &recSub{name: "a", failWith: fmt.Errorf("boom")}
	b := &recSub{name: "b"}
	if err := r.Register(a); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := r.Register(b); err != nil {
		t.Fatalf("Register b: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	if err := r.Trigger("x"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForMessages(t, buf, summaryMsg, 1, 2*time.Second)

	if a.count() != 1 || b.count() != 1 {
		t.Errorf("counts a=%d b=%d, want 1 each (실패 격리)", a.count(), b.count())
	}
	sum := linesWithMessage(buf, summaryMsg)[0]
	if got := toStrings(sum["failed"]); !equalStrings(got, []string{"a"}) {
		t.Errorf("failed = %v, want [a]", got)
	}
	if got := toStrings(sum["succeeded"]); !equalStrings(got, []string{"b"}) {
		t.Errorf("succeeded = %v, want [b]", got)
	}
	if r.lastApplied() == nil {
		t.Error("lastApplied = nil, want 갱신 (부분 성공)")
	}
	// 부분 실패 시 요약은 warn 레벨로 승격되어 운영자가 성공으로 오인하지 않게 한다
	// (adversarial finding 보완, best-effort/lastApplied 의미는 FR-060c 대로 유지).
	if sum["level"] != "warn" {
		t.Errorf("level = %v, want warn (부분 실패)", sum["level"])
	}
}

// TestReloader_Idempotency 는 TS-71: 동일 설정 2회 reload 시 Apply 는 매번 호출됨을 본다.
func TestReloader_Idempotency(t *testing.T) {
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

	if err := r.Trigger("1"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForCount(t, a.count, 1, 2*time.Second)
	if err := r.Trigger("2"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForCount(t, a.count, 2, 2*time.Second)
}

// TestReloader_DebounceCoalesces 는 TS-72: debounce 창 내 3회 trigger 가 1회 reload 로
// 병합되고 trigger_sources 에 3개가 모두 기록되는지 fakeClock 으로 결정론 검증한다.
func TestReloader_DebounceCoalesces(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	clock := newFakeClock()
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 200},
		WithErrorLogger(newTestLogger(buf)), WithClock(clock))
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

	if err := r.Trigger("s1"); err != nil {
		t.Fatalf("Trigger s1: %v", err)
	}
	timer := <-clock.newCh // worker 가 첫 trigger 를 읽고 타이머 생성
	if err := r.Trigger("s2"); err != nil {
		t.Fatalf("Trigger s2: %v", err)
	}
	<-timer.resetCh
	if err := r.Trigger("s3"); err != nil {
		t.Fatalf("Trigger s3: %v", err)
	}
	<-timer.resetCh

	timer.fire()
	waitForMessages(t, buf, summaryMsg, 1, 2*time.Second)

	if a.count() != 1 {
		t.Errorf("apply count = %d, want 1 (debounce 병합)", a.count())
	}
	sum := linesWithMessage(buf, summaryMsg)[0]
	if got := toStrings(sum["trigger_sources"]); !equalStrings(got, []string{"s1", "s2", "s3"}) {
		t.Errorf("trigger_sources = %v, want [s1 s2 s3]", got)
	}
}

// TestReloader_RegisterAfterStart 는 TS-73: Start 이후 Register 거부를 본다.
func TestReloader_RegisterAfterStart(t *testing.T) {
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
	defer r.Stop()

	if err := r.Register(&recSub{name: "late"}); err == nil {
		t.Error("Register after Start: err = nil, want ErrAlreadyStarted")
	}
}

// TestReloader_RegisterValidation 은 TS-74: nil/빈 이름/중복 거부를 본다.
func TestReloader_RegisterValidation(t *testing.T) {
	t.Parallel()
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0})
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	if err := r.Register(nil); err == nil {
		t.Error("Register(nil): err = nil, want error")
	}
	if err := r.Register(&recSub{name: ""}); err == nil {
		t.Error("Register(empty name): err = nil, want error")
	}
	if err := r.Register(&recSub{name: "dup"}); err != nil {
		t.Fatalf("Register dup#1: %v", err)
	}
	if err := r.Register(&recSub{name: "dup"}); err == nil {
		t.Error("Register(dup): err = nil, want error")
	}
}

// TestReloader_StopIdempotent 는 TS-75: Stop 2회 호출 안전을 본다.
func TestReloader_StopIdempotent(t *testing.T) {
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
		t.Errorf("Stop#1: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Errorf("Stop#2: %v", err)
	}
}

// TestReloader_TriggerBeforeStart 는 TS-76a(SPEC-009): Start 전 Trigger → ErrNotStarted.
func TestReloader_TriggerBeforeStart(t *testing.T) {
	t.Parallel()
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0})
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	if err := r.Trigger("manual"); err == nil {
		t.Error("Trigger before Start: err = nil, want ErrNotStarted")
	}
}

// TestReloader_CtxCancelStops 는 TS-76: Start(ctx) 후 cancel 시 goroutine 이 모두 종료되는지
// (Stop 이 deadlock 없이 반환) 검증한다.
func TestReloader_CtxCancelStops(t *testing.T) {
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

	cancel() // ctx 취소 → 자동 Stop 경로

	done := make(chan error, 1)
	go func() { done <- r.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return within 3s after ctx cancel (deadlock 의심)")
	}
}

// TestReloader_CoalesceDuringDispatch 는 TS-76b(SPEC-003 + REV2-002): dispatch 중 도착한
// trigger 들이 다음 사이클로 병합되고 source 가 모두 집계되는지 검증한다.
func TestReloader_CoalesceDuringDispatch(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML())
	r, err := NewReloader(path, config.ReloadConfig{DebounceMs: 0}, WithErrorLogger(newTestLogger(buf)))
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	sub := &recSub{name: "blocker", entered: make(chan struct{}), release: make(chan struct{})}
	if err := r.Register(sub); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop()

	if err := r.Trigger("first"); err != nil {
		t.Fatalf("Trigger first: %v", err)
	}
	<-sub.entered // 첫 dispatch 진입(블록 중)

	// dispatch 중 추가 trigger 3회
	for _, s := range []string{"a", "b", "c"} {
		if err := r.Trigger(s); err != nil {
			t.Fatalf("Trigger %s: %v", s, err)
		}
	}
	sub.release <- struct{}{} // 첫 Apply 해제

	<-sub.entered             // 두 번째 사이클 dispatch 진입
	sub.release <- struct{}{} // 두 번째 Apply 해제

	waitForMessages(t, buf, summaryMsg, 2, 2*time.Second)
	summaries := linesWithMessage(buf, summaryMsg)
	if got := toStrings(summaries[0]["trigger_sources"]); !equalStrings(got, []string{"first"}) {
		t.Errorf("1st trigger_sources = %v, want [first]", got)
	}
	if got := toStrings(summaries[1]["trigger_sources"]); !equalStrings(got, []string{"a", "b", "c"}) {
		t.Errorf("2nd trigger_sources = %v, want [a b c]", got)
	}
}

// TestReloader_RestartFieldWarningOnce 는 TS-76c(REV4-002): log.format 변경 시 warning 이
// 정확히 1건 기록되는지 검증한다.
func TestReloader_RestartFieldWarningOnce(t *testing.T) {
	t.Parallel()
	buf := &safeBuffer{}
	path := newConfigFile(t, defaultYAML()) // boot: format json
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

	writeYAMLFile(t, path, renderYAML("info", "console", 8080, "30s", "30s")) // format json→console
	if err := r.Trigger("fmt"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForMessages(t, buf, summaryMsg, 1, 2*time.Second)

	warns := linesWithMessage(buf, "restart-required config fields changed; ignored until restart")
	if len(warns) != 1 {
		t.Fatalf("restart warning count = %d, want 1\nlog:\n%s", len(warns), buf.String())
	}
}

// TestReloader_EnvOverridePreserved 는 TS-76d(REV5-002): config.Load 재사용으로 reload 후에도
// 환경변수 오버라이드가 유지되는지 검증한다.
func TestReloader_EnvOverridePreserved(t *testing.T) {
	// t.Setenv 는 t.Parallel 과 함께 쓸 수 없다.
	buf := &safeBuffer{}
	t.Setenv("SERVER_REQUEST_TIMEOUT", "5s")
	path := newConfigFile(t, defaultYAML()) // YAML request_timeout: 30s
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

	// YAML 을 request_timeout: 10s 로 변경 + trigger → env(5s) 가 유지되어야 함
	writeYAMLFile(t, path, renderYAML("info", "json", 8080, "10s", "30s"))
	if err := r.Trigger("env"); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	waitForCount(t, a.count, 1, 2*time.Second)

	if got := a.lastCfg().Server.RequestTimeout; got != 5*time.Second {
		t.Errorf("reload 후 request_timeout = %s, want 5s (env override 유지)", got)
	}
}
