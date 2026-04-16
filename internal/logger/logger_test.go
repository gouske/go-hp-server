// Package logger 테스트는 FEATURE_SPEC.md 의 TS-09 ~ TS-10 시나리오를 포함하며,
// WithWriter 로 출력 대상을 가로채 실제 출력 포맷을 검증한다.
package logger

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/gouske/go-hp-server/internal/config"
)

// TestNew_JSONFormat 은 TS-09: Format="json" 일 때 반환된 로거가
// 1줄의 JSON 이벤트를 기록하는지 구조적으로 검증한다.
func TestNew_JSONFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	lg, err := New(config.LogConfig{Level: "info", Format: "json"}, WithWriter(&buf))
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if lg == nil {
		t.Fatal("New() returned nil logger")
	}

	lg.Info().Str("key", "value").Msg("hello")

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("expected JSON output, got empty buffer")
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v, want nil", line, err)
	}

	tests := []struct {
		name string
		key  string
		want any
	}{
		{"level field", "level", "info"},
		{"message field", "message", "hello"},
		{"extra field", "key", "value"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := event[tc.key]
			if !ok {
				t.Fatalf("event missing key %q: %v", tc.key, event)
			}
			if got != tc.want {
				t.Errorf("event[%q] = %v, want %v", tc.key, got, tc.want)
			}
		})
	}

	if _, ok := event["time"]; !ok {
		t.Errorf("event missing %q field (Timestamp 체인 확인 필요): %v", "time", event)
	}
}

// TestNew_ConsoleFormat 은 Format="console" 일 때 zerolog.ConsoleWriter 가
// 적용되어 JSON 이 아닌 사람이 읽기 쉬운 텍스트 라인을 출력하는지 확인한다.
func TestNew_ConsoleFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	lg, err := New(config.LogConfig{Level: "debug", Format: "console"}, WithWriter(&buf))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	lg.Info().Msg("hello-console")

	out := buf.String()
	if out == "" {
		t.Fatal("expected console output, got empty buffer")
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("console output looks like JSON: %q", out)
	}
	if !strings.Contains(out, "hello-console") {
		t.Errorf("console output missing message: %q", out)
	}
}

// TestNew_LevelFiltersLowerLevels 는 Level 이 warn 일 때 info/debug 이벤트가
// 버려지고 warn 이상만 출력되는지 확인한다.
func TestNew_LevelFiltersLowerLevels(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer

	lg, err := New(config.LogConfig{Level: "warn", Format: "json"}, WithWriter(&buf))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	lg.Debug().Msg("dropped-debug")
	lg.Info().Msg("dropped-info")
	lg.Warn().Msg("kept-warn")

	out := buf.String()
	if strings.Contains(out, "dropped-debug") || strings.Contains(out, "dropped-info") {
		t.Errorf("lower-level events should be filtered, got: %q", out)
	}
	if !strings.Contains(out, "kept-warn") {
		t.Errorf("warn event missing from output: %q", out)
	}
}

// TestNew_InvalidInputs 는 TS-10 을 포함해 허용 집합 밖의 Level/Format 입력 시
// panic 없이 `logger new:` 접두 에러가 반환되는지 확인한다.
func TestNew_InvalidInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     config.LogConfig
		wantSub string
	}{
		{
			name:    "invalid level (TS-10)",
			cfg:     config.LogConfig{Level: "invalid", Format: "json"},
			wantSub: "invalid log level",
		},
		{
			name:    "empty level",
			cfg:     config.LogConfig{Level: "", Format: "json"},
			wantSub: "invalid log level",
		},
		{
			name:    "invalid format",
			cfg:     config.LogConfig{Level: "info", Format: "xml"},
			wantSub: "invalid log format",
		},
		{
			name:    "empty format",
			cfg:     config.LogConfig{Level: "info", Format: ""},
			wantSub: "invalid log format",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lg, err := New(tc.cfg)
			if err == nil {
				t.Fatalf("New(%+v) err = nil, want error containing %q", tc.cfg, tc.wantSub)
			}
			if lg != nil {
				t.Errorf("New(%+v) logger = %v, want nil on error", tc.cfg, lg)
			}
			if !strings.HasPrefix(err.Error(), "logger new:") {
				t.Errorf("err = %q, want prefix %q", err.Error(), "logger new:")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestNew_StrictLevelFormat 은 Level/Format 이 대문자나 앞뒤 공백을 포함하면
// New 가 `logger new:` 접두 에러를 반환하는지 확인한다. 정규화 책임은 호출자
// (config.Validate)에 있으며, 본 패키지는 엄격 매칭만 수행한다.
func TestNew_StrictLevelFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     config.LogConfig
		wantSub string
	}{
		{"uppercase level rejected", config.LogConfig{Level: "INFO", Format: "json"}, "invalid log level"},
		{"mixed case format rejected", config.LogConfig{Level: "info", Format: "Console"}, "invalid log format"},
		{"whitespace level rejected", config.LogConfig{Level: "  warn  ", Format: "json"}, "invalid log level"},
		{"whitespace format rejected", config.LogConfig{Level: "info", Format: " json "}, "invalid log format"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lg, err := New(tc.cfg)
			if err == nil {
				t.Fatalf("New(%+v) err = nil, want error containing %q", tc.cfg, tc.wantSub)
			}
			if lg != nil {
				t.Errorf("New(%+v) logger = %v, want nil on error", tc.cfg, lg)
			}
			if !strings.HasPrefix(err.Error(), "logger new:") {
				t.Errorf("err = %q, want prefix %q", err.Error(), "logger new:")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// TestWithWriter_NilKeepsDefault 는 WithWriter(nil) 이 기본 Writer 를
// 유지하는지 확인한다 (덮어쓰면 os.Stdout 이 nil 이 되어 panic 위험).
func TestWithWriter_NilKeepsDefault(t *testing.T) {
	t.Parallel()
	o := &options{writer: io.Discard}
	WithWriter(nil)(o)
	if o.writer != io.Discard {
		t.Errorf("WithWriter(nil) overwrote default writer: got=%v", o.writer)
	}
}

// TestParseLevel 은 parseLevel 의 경계 케이스를 직접 검증한다.
func TestParseLevel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input   string
		want    zerolog.Level
		wantErr bool
	}{
		{"debug", zerolog.DebugLevel, false},
		{"info", zerolog.InfoLevel, false},
		{"warn", zerolog.WarnLevel, false},
		{"error", zerolog.ErrorLevel, false},
		{"DEBUG", zerolog.NoLevel, true},
		{"  info  ", zerolog.NoLevel, true},
		{"trace", zerolog.NoLevel, true},
		{"", zerolog.NoLevel, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			got, err := parseLevel(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseLevel(%q) err = nil, want error", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLevel(%q) err = %v, want nil", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("parseLevel(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
