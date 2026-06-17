// Package logger 의 LevelSubscriber 테스트는 P0-4 의 TS-60 ~ TS-62 를 커버한다.
// zerolog 전역 레벨을 변경하므로 모든 테스트는 t.Parallel 을 사용하지 않고
// t.Cleanup 으로 원복한다.
package logger

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
)

// cfgWithLog 는 LevelSubscriber 테스트용 최소 Config 를 만든다.
func cfgWithLog(level, format string) *config.Config {
	c := &config.Config{}
	c.Log.Level = level
	c.Log.Format = format
	return c
}

// TestLevelSubscriber_Apply_AppliesGlobalLevel 은 TS-60: Apply 후 전역 레벨이
// 갱신되어 기존 로거 포인터가 즉시 새 레벨 필터를 반영하는지 검증한다.
func TestLevelSubscriber_Apply_AppliesGlobalLevel(t *testing.T) {
	prev := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	var buf bytes.Buffer
	lg, err := New(config.LogConfig{Level: "info", Format: "json"}, WithWriter(&buf))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	lg.Debug().Msg("before-apply")
	if strings.Contains(buf.String(), "before-apply") {
		t.Fatalf("debug must be filtered at info level, got: %q", buf.String())
	}

	sub := NewLevelSubscriber()
	if sub == nil {
		t.Fatal("NewLevelSubscriber() returned nil")
	}
	if sub.Name() != "log_level" {
		t.Errorf("Name() = %q, want %q", sub.Name(), "log_level")
	}
	if err := sub.Apply(context.Background(), cfgWithLog("debug", "json")); err != nil {
		t.Fatalf("Apply() error = %v, want nil", err)
	}

	lg.Debug().Msg("after-apply")
	if !strings.Contains(buf.String(), "after-apply") {
		t.Errorf("debug must be emitted after Apply(level=debug), got: %q", buf.String())
	}
}

// TestLevelSubscriber_Apply_IgnoresFormat 는 TS-61: format 변경은 LevelSubscriber 의
// 책임이 아니며 Apply 는 레벨만 반영하고 에러 없이 반환한다.
func TestLevelSubscriber_Apply_IgnoresFormat(t *testing.T) {
	prev := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	sub := NewLevelSubscriber()
	if err := sub.Apply(context.Background(), cfgWithLog("warn", "console")); err != nil {
		t.Fatalf("Apply() error = %v, want nil (format 은 무시되어야 함)", err)
	}
	if zerolog.GlobalLevel() != zerolog.WarnLevel {
		t.Errorf("GlobalLevel() = %v, want warn", zerolog.GlobalLevel())
	}
}

// TestLevelSubscriber_Apply_InvalidLevel 은 허용 집합 밖 레벨 시 panic 없이 에러를 반환하는지 본다.
func TestLevelSubscriber_Apply_InvalidLevel(t *testing.T) {
	prev := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	sub := NewLevelSubscriber()
	if err := sub.Apply(context.Background(), cfgWithLog("trace", "json")); err == nil {
		t.Fatal("Apply() error = nil, want error for invalid level")
	}
}

// TestLevelSubscriber_Apply_NilConfig 는 nil Config 시 panic 없이 에러를 반환하는지 본다.
func TestLevelSubscriber_Apply_NilConfig(t *testing.T) {
	prev := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	sub := NewLevelSubscriber()
	if err := sub.Apply(context.Background(), nil); err == nil {
		t.Fatal("Apply(nil) err = nil, want error")
	}
}

// TestLevelSubscriber_Apply_Race 는 TS-62: 다수 고루틴이 로그를 호출하는 동안
// main 이 Apply 로 레벨을 토글해도 race/panic 이 없는지 -race 로 검증한다.
func TestLevelSubscriber_Apply_Race(t *testing.T) {
	prev := zerolog.GlobalLevel()
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	lg, err := New(config.LogConfig{Level: "debug", Format: "json"}, WithWriter(io.Discard))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sub := NewLevelSubscriber()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				lg.Info().Int("j", j).Msg("concurrent")
			}
		}()
	}

	for i := 0; i < 10; i++ {
		level := "info"
		if i%2 == 0 {
			level = "debug"
		}
		if err := sub.Apply(context.Background(), cfgWithLog(level, "json")); err != nil {
			t.Errorf("Apply() error = %v", err)
		}
	}

	wg.Wait()
}
