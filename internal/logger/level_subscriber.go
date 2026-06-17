package logger

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
)

// LevelSubscriber 는 reload.Subscriber 로서 cfg.Log.Level 을 zerolog 전역 레벨로 반영한다.
//
// 구현은 zerolog.SetGlobalLevel(newLevel) 한 번뿐이다. zerolog 가 제공하는 전역 atomic
// 필터이므로 기존에 생성된 모든 로거 포인터가 즉시 새 레벨을 반영한다(atomic.Pointer 스왑
// 같은 별도 저장소가 필요 없다). 따라서 Apply 비용은 사실상 atomic store 1회다.
//
// 책임 범위 (P0-4 REV4-002): LevelSubscriber 는 오직 log.level 만 처리한다.
// log.format 변경은 Reloader 의 공통 "재시작 필요 필드" warning 로직이 전담하며,
// 본 타입은 format 을 읽지도, 그에 대한 경고를 남기지도 않는다.
type LevelSubscriber struct{}

// NewLevelSubscriber 는 LevelSubscriber 를 생성한다.
// 현 구현상 내부 상태가 없어 인자 없이 생성 가능하지만, 장래 확장을 위해 생성자 형태를 유지한다.
func NewLevelSubscriber() *LevelSubscriber {
	return &LevelSubscriber{}
}

// Name 은 Subscriber 식별자 "log_level" 을 반환한다.
func (s *LevelSubscriber) Name() string {
	return "log_level"
}

// Apply 는 cfg.Log.Level 을 zerolog.Level 로 파싱해 zerolog.SetGlobalLevel 로 반영한다.
// 파싱 실패 시 `logger level_subscriber:` 접두 에러를 반환하며 panic 하지 않는다.
//
// ctx 는 Subscriber 인터페이스 계약상 받지만, 본 구현은 즉시 완료되므로 별도 취소 처리는 없다.
// cfg 가 nil 이면 에러를 반환한다.
func (s *LevelSubscriber) Apply(_ context.Context, cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("logger level_subscriber: nil config")
	}
	level, err := parseLevel(cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("logger level_subscriber: %w", err)
	}
	zerolog.SetGlobalLevel(level)
	return nil
}
