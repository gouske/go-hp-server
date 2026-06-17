// Package reload 는 서버 재시작 없이 선택된 설정 필드를 런타임에 반영하는
// file-based hot-reload 를 제공한다(P0-4).
//
// 핵심 구성요소는 Reloader(trigger 수신 → debounce → config.Load → Validate → Subscriber
// dispatch) 와 Subscriber(각 도메인 패키지가 자신의 atomic 상태를 갱신) 다. 동시성 모델은
// 단일 worker goroutine 이 triggerCh(버퍼 1)를 소비하고, 모든 trigger 호출자는 채널 send
// 성공 여부와 무관하게 debounce source set 에 source 를 추가한다. 따라서 채널 coalescing 으로
// 인해 일부 wakeup 이 합쳐지더라도 trigger_sources 집계는 누락되지 않는다.
//
// # (D) 실무 대안: 관리 엔드포인트 패턴 (FR-060k)
//
// 본 패키지는 학습 목적으로 file watch / SIGHUP 기반 generic hot-reload 를 구현하지만,
// 현대 클라우드 네이티브 환경에서는 이 방식이 종종 과하다. 실무에서는 아래 대안(D)을 먼저
// 검토하기를 강력히 권장한다.
//
// 왜 file-based reload 가 K8s 환경에서 과한가:
//   - Kubernetes + ConfigMap + rolling update + readiness probe(/ready, P0-5) 조합은
//     Pod 를 점진 교체하면서 무중단으로 설정을 바꿀 수 있다. 즉 "프로세스 내부 hot-reload"
//     없이도 무중단 설정 변경이 가능하다.
//   - generic file watch 는 (a) 변경 가능 필드/불가 필드 경계를 코드로 강제해야 하고,
//     (b) atomic-save 연쇄 이벤트 debounce, (c) 부분 적용 실패 시 상태 일관성,
//     (d) trigger 보안 경계 등 부수 복잡도가 크다. 폭발 반경이 넓다.
//
// 대안 구현 스케치 — 관리 엔드포인트:
//
//	// 인증 미들웨어(P2-11) 뒤에 배치하고 감사 로그를 남긴다.
//	PUT /admin/log-level  {"level": "debug"}
//	  → 검증 후 zerolog.SetGlobalLevel(debug) 한 줄. 변경 주체/시각이 audit 으로 남는다.
//
// 이 방식은 (1) 변경 가능한 항목이 엔드포인트 단위로 명시적이고, (2) 인증/인가/감사가
// 표준 HTTP 미들웨어로 자연스럽게 붙으며, (3) 파일시스템/시그널 의존이 없어 테스트가 쉽고,
// (4) 폭발 반경이 작다(엔드포인트 1개 = 필드 1개).
//
// 참조 사례:
//   - Envoy: xDS(동적 설정 API) 로 런타임 구성, 파일 watch 가 아닌 gRPC/REST 컨트롤 플레인.
//   - Prometheus: SIGHUP 또는 POST /-/reload 로 설정 재적용(본 패키지의 SIGHUP 과 유사).
//   - HashiCorp Vault: SIGHUP 으로 일부 설정 재적용, 그 외는 API 기반.
//
// 본 패키지를 실무로 포팅할 때 체크리스트:
//   - [ ] hot-reload 가능 필드를 화이트리스트로 코드에 고정했는가(임의 필드 reload 금지).
//   - [ ] reload trigger 에 인증/인가 경계가 있는가(file watch 라면 파일 권한·소유자 고정).
//   - [ ] 변경 감사 로그(누가/언제/무엇을)를 남기는가.
//   - [ ] 부분 적용 실패 시 관측 가능한 상태(요약 로그의 succeeded/failed)를 노출하는가.
//   - [ ] K8s 환경이라면 ConfigMap + rolling update 로 충분하지 않은지 먼저 재검토했는가.
package reload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
)

// trigger source 화이트리스트 값.
const (
	sourceSighup = "sighup"
	sourceFile   = "file"
)

// 공개 sentinel 에러. 호출부는 errors.Is 로 판별한다.
var (
	ErrAlreadyStarted = errors.New("reload: reloader already started")
	ErrNotStarted     = errors.New("reload: reloader not started")
	ErrInvalidSource  = errors.New("reload: invalid trigger source")
	// ErrStopped 는 Stop 이후 Trigger 호출에 반환한다. FR-060b1 에 따라 ErrNotStarted 는
	// Start 이전 전용이므로, 종료 후 trigger 는 별도 에러로 구분한다.
	ErrStopped = errors.New("reload: reloader stopped")
)

// Subscriber 는 reload 시 새 Config 의 일부를 자신의 상태에 반영하는 구현체이다.
// Apply 는 atomic swap 계열의 lock-free 갱신만 수행해야 하며 에러 반환이 가능하다.
type Subscriber interface {
	// Name 은 로깅·등록 중복 검사용 식별자이다.
	Name() string
	// Apply 는 새 설정을 반영한다. ctx 는 Reloader 가 주입하며 취소되면 빠르게 반환해야 한다.
	// 반환 에러는 best-effort dispatch 규칙에 따라 로깅되고 다른 Subscriber 호출은 계속된다.
	Apply(ctx context.Context, cfg *config.Config) error
}

// Clock 은 debounce 타이머를 테스트에서 결정론적으로 제어하기 위한 인터페이스이다.
type Clock interface {
	NewTimer(d time.Duration) Timer
}

// Timer 는 Clock 이 생성하는 타이머 추상화이다.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration)
}

// realClock 은 표준 time.Timer 기반 기본 Clock 이다.
type realClock struct{}

func (realClock) NewTimer(d time.Duration) Timer { return &realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (rt *realTimer) C() <-chan time.Time   { return rt.t.C }
func (rt *realTimer) Stop() bool            { return rt.t.Stop() }
func (rt *realTimer) Reset(d time.Duration) { rt.t.Reset(d) }

// reloaderConfig 는 ReloaderOption 이 채우는 내부 설정이다.
type reloaderConfig struct {
	errorLogger *zerolog.Logger
	clock       Clock
}

// ReloaderOption 은 Reloader 생성 시 함수형 옵션이다. 검증 실패 시 error 를 반환한다.
type ReloaderOption func(*reloaderConfig) error

// WithErrorLogger 는 reload 과정의 에러·경고·요약을 기록할 로거를 주입한다(nil 거부).
func WithErrorLogger(l *zerolog.Logger) ReloaderOption {
	return func(rc *reloaderConfig) error {
		if l == nil {
			return errors.New("reload: error logger must not be nil")
		}
		rc.errorLogger = l
		return nil
	}
}

// WithClock 은 debounce 타이머 테스트용 시계를 주입한다(미지정 시 real clock).
func WithClock(c Clock) ReloaderOption {
	return func(rc *reloaderConfig) error {
		if c == nil {
			return errors.New("reload: clock must not be nil")
		}
		rc.clock = c
		return nil
	}
}

// Reloader 는 설정 파일 trigger 를 감시해 등록된 Subscriber 들에 새 설정을 dispatch 한다.
// 모든 필드는 비공개이며 NewReloader 를 통해서만 초기화된다.
type Reloader struct {
	configPath string
	sources    []string
	debounce   time.Duration
	logger     *zerolog.Logger
	clock      Clock
	bootCfg    *config.Config

	mu          sync.Mutex
	subscribers []Subscriber
	subNames    map[string]struct{}
	started     bool
	stopped     bool

	triggerCh chan string

	srcMu  sync.Mutex
	srcSet map[string]struct{}

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	lastMu            sync.Mutex
	lastAppliedConfig *config.Config
}

// NewReloader 는 지정된 설정 파일 경로를 감시하는 Reloader 를 생성한다.
//
// configPath 가 비어 있거나 존재하지 않으면 에러. rc.Sources 의 값 화이트리스트
// ({"sighup","file"}) 검증을 여기서 수행하며(REV4-001 import cycle 회피), 위반 시
// ErrInvalidSource. opts 검증 실패 시 에러. 부팅 baseline 으로 configPath 를 1회 로드·검증해
// 재시작 필요 필드 비교의 기준값으로 삼는다. panic 하지 않는다.
func NewReloader(configPath string, rc config.ReloadConfig, opts ...ReloaderOption) (*Reloader, error) {
	if strings.TrimSpace(configPath) == "" {
		return nil, errors.New("reload: config path is empty")
	}
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("reload: config path: %w", err)
	}
	for _, s := range rc.Sources {
		if s != sourceSighup && s != sourceFile {
			return nil, fmt.Errorf("%w: %q", ErrInvalidSource, s)
		}
	}

	rcfg := &reloaderConfig{}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(rcfg); err != nil {
			return nil, err
		}
	}
	logger := rcfg.errorLogger
	if logger == nil {
		nop := zerolog.Nop()
		logger = &nop
	}
	clock := rcfg.clock
	if clock == nil {
		clock = realClock{}
	}

	bootCfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("reload: load boot config: %w", err)
	}
	// 재시작 필요 필드 비교를 위해 boot 값도 정규화(log.format 등)·검증한다.
	if err := bootCfg.Validate(); err != nil {
		return nil, fmt.Errorf("reload: validate boot config: %w", err)
	}

	// sources 슬라이스는 호출부 변형으로부터 보호하기 위해 복사한다.
	srcCopy := append([]string(nil), rc.Sources...)

	return &Reloader{
		configPath:        configPath,
		sources:           srcCopy,
		debounce:          time.Duration(rc.DebounceMs) * time.Millisecond,
		logger:            logger,
		clock:             clock,
		bootCfg:           bootCfg,
		subNames:          make(map[string]struct{}),
		lastAppliedConfig: bootCfg,
	}, nil
}

// Register 는 Subscriber 를 추가한다. Start 이전에만 호출 가능하다.
// sub==nil, sub.Name()=="", 중복 이름은 모두 error. Start/Stop 이후 호출은 ErrAlreadyStarted.
func (r *Reloader) Register(sub Subscriber) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || r.stopped {
		return ErrAlreadyStarted
	}
	if sub == nil {
		return errors.New("reload: subscriber is nil")
	}
	name := sub.Name()
	if name == "" {
		return errors.New("reload: subscriber name is empty")
	}
	if _, dup := r.subNames[name]; dup {
		return fmt.Errorf("reload: duplicate subscriber name: %q", name)
	}
	r.subNames[name] = struct{}{}
	r.subscribers = append(r.subscribers, sub)
	return nil
}

// Start 는 설정된 trigger 들과 내부 worker 를 시작한다.
// 이미 시작되었거나 Stop 이후면 ErrAlreadyStarted. ctx 가 nil 이면 에러.
// ctx.Done() 발화 시 자동으로 Stop 경로로 진입한다.
func (r *Reloader) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("reload: context is nil")
	}
	r.mu.Lock()
	if r.started || r.stopped {
		r.mu.Unlock()
		return ErrAlreadyStarted
	}
	r.started = true
	r.triggerCh = make(chan string, 1)
	r.srcSet = make(map[string]struct{})
	r.stopCh = make(chan struct{})
	r.mu.Unlock()

	r.wg.Add(1)
	go r.worker(ctx)

	// ctx 자동 Stop watcher. stopCh 가 닫히면 함께 종료된다(재진입 Stop 호출 없음).
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		select {
		case <-ctx.Done():
			r.signalStop()
		case <-r.stopCh:
		}
	}()

	for _, s := range r.sources {
		switch s {
		case sourceSighup:
			r.startSighupTrigger()
		case sourceFile:
			if err := r.startFileTrigger(); err != nil {
				r.signalStop()
				r.wg.Wait()
				r.mu.Lock()
				r.started = false
				r.stopped = true
				r.mu.Unlock()
				return fmt.Errorf("reload: start file trigger: %w", err)
			}
		}
	}
	return nil
}

// Stop 은 모든 trigger 와 worker 를 종료하고 진행 중 reload 가 끝날 때까지 대기한 뒤 반환한다.
// 여러 번 호출해도 안전하며, 두 번째 이후 호출은 no-op nil 을 반환한다.
//
// Start 전 호출은 상태를 오염시키지 않는 진짜 no-op 이다. 즉 Stop 을 먼저 부르더라도
// stopped 플래그를 세우지 않으므로 이후 Register/Start 가 정상 동작한다(MAJOR-1).
func (r *Reloader) Stop() error {
	r.mu.Lock()
	if !r.started {
		// 시작된 적 없으면 종료할 worker/trigger 가 없다. 상태를 건드리지 않고 반환한다.
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	r.signalStop()
	r.wg.Wait()
	return nil
}

// signalStop 은 종료를 한 번만 신호한다. stopped=true 를 설정하고 stopCh 를 닫는다.
// 명시적 Stop 과 ctx 취소 watcher 모두 이 경로를 공유하므로, 어느 종료 경로에서든
// 이후 Trigger 가 ErrStopped 를 반환한다(MAJOR-2/MAJOR-3 silent drop 방지).
func (r *Reloader) signalStop() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.stopped = true
		r.mu.Unlock()
		close(r.stopCh)
	})
}

// Trigger 는 외부(테스트·관리 엔드포인트)에서 명시적으로 reload 를 유발한다.
// source 는 debounce window 의 source set 에 추가되어 요약 로그의 trigger_sources 에 집계된다.
//
// Start 이전 호출은 ErrNotStarted 를 반환한다(FR-060b1, 유일한 ErrNotStarted 반환 경로).
// Stop 이후 호출은 worker 가 이미 종료돼 silent drop 이 되므로 ErrStopped 를 반환한다(MAJOR-2).
func (r *Reloader) Trigger(source string) error {
	r.mu.Lock()
	started, stopped := r.started, r.stopped
	r.mu.Unlock()
	if !started {
		return ErrNotStarted
	}
	if stopped {
		return ErrStopped
	}
	r.enqueue(source)
	return nil
}

// enqueue 는 source 를 set 에 추가하고 worker 에 non-blocking wakeup 을 보낸다.
// 채널이 가득 차도 set 추가는 항상 수행되므로 source 집계가 누락되지 않는다(FR-060a).
func (r *Reloader) enqueue(source string) {
	r.srcMu.Lock()
	r.srcSet[source] = struct{}{}
	r.srcMu.Unlock()
	select {
	case r.triggerCh <- source:
	default:
	}
}

// worker 는 단일 goroutine 으로 trigger 를 소비해 debounce → reload 를 순차 수행한다.
// 종료 조건은 stopCh 닫힘(또는 ctx 취소로 인한 stopCh 닫힘)이다.
func (r *Reloader) worker(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-r.stopCh:
			return
		case <-r.triggerCh:
			if !r.debounceWait() {
				return
			}
			r.doReload(ctx)
		}
	}
}

// debounceWait 는 debounce 창을 기다리며 추가 trigger 도착 시 타이머를 리셋한다.
// debounce<=0 이면 즉시 true 를 반환한다. stopCh 닫힘 시 false 를 반환한다.
func (r *Reloader) debounceWait() bool {
	if r.debounce <= 0 {
		return true
	}
	timer := r.clock.NewTimer(r.debounce)
	defer timer.Stop()
	for {
		select {
		case <-r.stopCh:
			return false
		case <-r.triggerCh:
			if !timer.Stop() {
				select {
				case <-timer.C():
				default:
				}
			}
			timer.Reset(r.debounce)
		case <-timer.C():
			return true
		}
	}
}

// doReload 는 source 스냅샷 → config.Load → Validate → 재시작필드 warning → Subscriber
// dispatch → lastAppliedConfig 갱신 → 요약 로그를 수행한다.
func (r *Reloader) doReload(ctx context.Context) {
	sources := r.snapshotSources()

	validateStart := time.Now()
	newCfg, err := config.Load(r.configPath)
	if err != nil {
		r.logger.Error().Err(err).Strs("trigger_sources", sources).Msg("config reload load failed")
		return
	}
	if err := newCfg.Validate(); err != nil {
		r.logger.Error().Err(err).Strs("trigger_sources", sources).Msg("config reload validation failed")
		return
	}
	validateMS := durationMS(time.Since(validateStart))

	r.warnRestartRequired(newCfg)

	dispatchStart := time.Now()
	succeeded := make([]string, 0, len(r.subscribers))
	failed := make([]string, 0)
	for _, sub := range r.subscribers {
		if err := sub.Apply(ctx, newCfg); err != nil {
			failed = append(failed, sub.Name())
			r.logger.Error().
				Str("checker", sub.Name()).
				Err(err).
				Msg("config reload subscriber apply failed")
			continue
		}
		succeeded = append(succeeded, sub.Name())
	}
	dispatchMS := durationMS(time.Since(dispatchStart))

	// 한 Subscriber 라도 성공하면 부분 반영 상태가 "현재 상태"이므로 lastAppliedConfig 갱신.
	if len(succeeded) > 0 {
		r.setLastApplied(newCfg)
	}

	// 요약 로그는 단일 라인(고정 필드)이되, 부분 실패(failed 비어있지 않음) 시 Warn 으로
	// 승격해 운영자/대시보드가 부분 적용을 성공으로 오인하지 않게 한다. best-effort dispatch 와
	// lastAppliedConfig 갱신 규칙은 FR-060c 대로 유지하며, 각 실패는 위에서 ERROR 로 기록된다.
	summary := r.logger.Info()
	if len(failed) > 0 {
		summary = r.logger.Warn()
	}
	summary.
		Strs("trigger_sources", sources).
		Strs("succeeded", succeeded).
		Strs("failed", failed).
		Float64("validate_duration_ms", validateMS).
		Float64("dispatch_duration_ms", dispatchMS).
		Msg("config reload applied")
}

// snapshotSources 는 현재 debounce source set 을 정렬된 슬라이스로 스냅샷하고 리셋한다.
func (r *Reloader) snapshotSources() []string {
	r.srcMu.Lock()
	defer r.srcMu.Unlock()
	out := make([]string, 0, len(r.srcSet))
	for s := range r.srcSet {
		out = append(out, s)
	}
	r.srcSet = make(map[string]struct{})
	sort.Strings(out)
	return out
}

// warnRestartRequired 는 boot 값과 달라진 재시작 필요 필드를 1건의 warning 으로 기록한다.
// reload 전체를 실패시키지 않으며 해당 필드는 무시된다(FR-060e).
func (r *Reloader) warnRestartRequired(n *config.Config) {
	b := r.bootCfg
	var changed []string
	add := func(field string, oldV, newV any) {
		changed = append(changed, fmt.Sprintf("%s: %v -> %v", field, oldV, newV))
	}
	if b.Server.Host != n.Server.Host {
		add("server.host", b.Server.Host, n.Server.Host)
	}
	if b.Server.Port != n.Server.Port {
		add("server.port", b.Server.Port, n.Server.Port)
	}
	if b.Server.ReadTimeout != n.Server.ReadTimeout {
		add("server.read_timeout", b.Server.ReadTimeout, n.Server.ReadTimeout)
	}
	if b.Server.WriteTimeout != n.Server.WriteTimeout {
		add("server.write_timeout", b.Server.WriteTimeout, n.Server.WriteTimeout)
	}
	if b.Server.IdleTimeout != n.Server.IdleTimeout {
		add("server.idle_timeout", b.Server.IdleTimeout, n.Server.IdleTimeout)
	}
	if b.Server.ReadHeaderTimeout != n.Server.ReadHeaderTimeout {
		add("server.read_header_timeout", b.Server.ReadHeaderTimeout, n.Server.ReadHeaderTimeout)
	}
	if b.Server.MaxHeaderBytes != n.Server.MaxHeaderBytes {
		add("server.max_header_bytes", b.Server.MaxHeaderBytes, n.Server.MaxHeaderBytes)
	}
	if b.Log.Format != n.Log.Format {
		add("log.format", b.Log.Format, n.Log.Format)
	}
	if !equalSources(b.Reload.Sources, n.Reload.Sources) {
		add("reload.sources", b.Reload.Sources, n.Reload.Sources)
	}
	if b.Reload.DebounceMs != n.Reload.DebounceMs {
		add("reload.debounce_ms", b.Reload.DebounceMs, n.Reload.DebounceMs)
	}
	if len(changed) > 0 {
		r.logger.Warn().
			Strs("changed_fields", changed).
			Msg("restart-required config fields changed; ignored until restart")
	}
}

// setLastApplied 는 현재 적용된 설정을 atomic 하게 교체한다.
func (r *Reloader) setLastApplied(cfg *config.Config) {
	r.lastMu.Lock()
	r.lastAppliedConfig = cfg
	r.lastMu.Unlock()
}

// lastApplied 는 마지막으로 (부분이라도) 적용된 설정을 반환한다(테스트·진단용).
func (r *Reloader) lastApplied() *config.Config {
	r.lastMu.Lock()
	defer r.lastMu.Unlock()
	return r.lastAppliedConfig
}

// durationMS 는 Duration 을 소수점 밀리초로 변환한다.
func durationMS(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// equalSources 는 두 source 슬라이스를 순서 무시 집합으로 비교한다(MINOR-2).
// reload.sources 는 NewReloader 에서 순서와 무관하게 처리되므로(각 원소를 sighup/file 로
// 등록) 단순 재정렬은 의미 있는 변경이 아니다. 따라서 재정렬만으로는 restart-required
// warning 이 발생하지 않도록 정렬 후 비교한다. (원소는 config.Validate 가 중복을 거부한다.)
func equalSources(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}
