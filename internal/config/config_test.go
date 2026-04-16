// Package config 테스트는 FEATURE_SPEC.md 의 TS-01 ~ TS-08 시나리오를
// table-driven 형식으로 커버하며, 환경변수 오버라이드는 t.Setenv 로
// 독립성을 확보한다.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sampleYAML 은 TS-01 ~ TS-04 의 기준 YAML 이며
// config/default.yaml 와 동일한 구조를 가진다.
const sampleYAML = `
server:
  host: "0.0.0.0"
  port: 8080
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  graceful_shutdown_timeout: 30s

worker_pool:
  size: 100
  queue_size: 10000

rate_limiter:
  enabled: true
  requests_per_second: 1000
  burst: 2000

circuit_breaker:
  enabled: true
  max_requests: 100
  interval: 60s
  timeout: 30s

metrics:
  enabled: true
  path: "/metrics"

log:
  level: "info"
  format: "json"
`

// writeTempYAML 은 임시 디렉터리에 content 내용을 담은 YAML 파일을
// 생성하고 경로를 반환한다. t.TempDir 이 자동으로 정리한다.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writeTempYAML: %v", err)
	}
	return path
}

// TestLoad_Success 는 TS-01: 정상 YAML 로드 시 모든 필드가 기대치와 일치하는지 확인한다.
func TestLoad_Success(t *testing.T) {
	t.Parallel()
	path := writeTempYAML(t, sampleYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg == nil {
		t.Fatal("Load() returned nil config")
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"server.host", cfg.Server.Host, "0.0.0.0"},
		{"server.port", cfg.Server.Port, 8080},
		{"server.read_timeout", cfg.Server.ReadTimeout, 30 * time.Second},
		{"server.write_timeout", cfg.Server.WriteTimeout, 30 * time.Second},
		{"server.idle_timeout", cfg.Server.IdleTimeout, 120 * time.Second},
		{"server.graceful_shutdown_timeout", cfg.Server.GracefulShutdownTimeout, 30 * time.Second},
		{"worker_pool.size", cfg.WorkerPool.Size, 100},
		{"worker_pool.queue_size", cfg.WorkerPool.QueueSize, 10000},
		{"rate_limiter.enabled", cfg.RateLimiter.Enabled, true},
		{"rate_limiter.requests_per_second", cfg.RateLimiter.RequestsPerSecond, 1000},
		{"rate_limiter.burst", cfg.RateLimiter.Burst, 2000},
		{"circuit_breaker.enabled", cfg.CircuitBreaker.Enabled, true},
		{"circuit_breaker.max_requests", cfg.CircuitBreaker.MaxRequests, 100},
		{"circuit_breaker.interval", cfg.CircuitBreaker.Interval, 60 * time.Second},
		{"circuit_breaker.timeout", cfg.CircuitBreaker.Timeout, 30 * time.Second},
		{"metrics.enabled", cfg.Metrics.Enabled, true},
		{"metrics.path", cfg.Metrics.Path, "/metrics"},
		{"log.level", cfg.Log.Level, "info"},
		{"log.format", cfg.Log.Format, "json"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("%s: got=%v, want=%v", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestLoad_PathErrors 는 TS-02: 경로 문제로 Load 가 에러를 반환하는지 검증한다.
func TestLoad_PathErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		path    string
		wantSub string
	}{
		{"empty path", "", "config load: path is empty"},
		{"blank path", "   ", "config load: path is empty"},
		{"missing file", filepath.Join(t.TempDir(), "not-exist.yaml"), "config load: read"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(tc.path)
			if err == nil {
				t.Fatalf("Load(%q) err = nil, want error containing %q", tc.path, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("Load(%q) err = %q, want substring %q", tc.path, err.Error(), tc.wantSub)
			}
		})
	}
}

// TestLoad_EnvOverride 는 TS-03, TS-04: 환경변수가 YAML 값을 덮어쓰는지 검증한다.
func TestLoad_EnvOverride(t *testing.T) {
	tests := []struct {
		name   string
		envKey string
		envVal string
		check  func(t *testing.T, cfg *Config)
	}{
		{
			name:   "SERVER_PORT overrides port",
			envKey: "SERVER_PORT",
			envVal: "9999",
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.Port != 9999 {
					t.Errorf("Server.Port = %d, want 9999", cfg.Server.Port)
				}
			},
		},
		{
			name:   "SERVER_READ_TIMEOUT overrides read_timeout",
			envKey: "SERVER_READ_TIMEOUT",
			envVal: "5s",
			check: func(t *testing.T, cfg *Config) {
				if cfg.Server.ReadTimeout != 5*time.Second {
					t.Errorf("Server.ReadTimeout = %s, want 5s", cfg.Server.ReadTimeout)
				}
			},
		},
		{
			name:   "LOG_LEVEL overrides log.level",
			envKey: "LOG_LEVEL",
			envVal: "debug",
			check: func(t *testing.T, cfg *Config) {
				if cfg.Log.Level != "debug" {
					t.Errorf("Log.Level = %q, want \"debug\"", cfg.Log.Level)
				}
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envKey, tc.envVal)
			path := writeTempYAML(t, sampleYAML)

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			tc.check(t, cfg)
		})
	}
}

// TestLoad_UnmarshalError 는 TS-05: YAML 타입 불일치 시 에러가 반환되는지 검증한다.
func TestLoad_UnmarshalError(t *testing.T) {
	t.Parallel()
	badYAML := `
server:
  host: "0.0.0.0"
  port: "not-a-number"
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 120s
  graceful_shutdown_timeout: 30s
worker_pool:
  size: 100
  queue_size: 10000
metrics:
  enabled: false
  path: "/metrics"
log:
  level: "info"
  format: "json"
`
	path := writeTempYAML(t, badYAML)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() err = nil, want unmarshal error")
	}
	if !strings.Contains(err.Error(), "config load: unmarshal") {
		t.Errorf("err = %q, want substring %q", err.Error(), "config load: unmarshal")
	}
}

// validConfig 는 Validate 테스트의 기본 통과 설정이며 각 케이스는 필요한 필드만 변경한다.
func validConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Host:                    "0.0.0.0",
			Port:                    8080,
			ReadTimeout:             30 * time.Second,
			WriteTimeout:            30 * time.Second,
			IdleTimeout:             120 * time.Second,
			GracefulShutdownTimeout: 30 * time.Second,
		},
		WorkerPool: WorkerPoolConfig{Size: 100, QueueSize: 10000},
		Metrics:    MetricsConfig{Enabled: true, Path: "/metrics"},
		Log:        LogConfig{Level: "info", Format: "json"},
	}
}

// TestValidate 는 TS-06 ~ TS-08 을 포함한 Validate 규칙 전체를 table-driven 으로 커버한다.
func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr bool
		wantSub string
	}{
		{
			name:   "valid config",
			mutate: func(c *Config) {},
		},
		{
			name:    "nil config",
			mutate:  nil,
			wantErr: true,
			wantSub: "nil config",
		},
		{
			name:    "empty host",
			mutate:  func(c *Config) { c.Server.Host = "   " },
			wantErr: true,
			wantSub: "server.host",
		},
		{
			name:    "port zero (TS-06)",
			mutate:  func(c *Config) { c.Server.Port = 0 },
			wantErr: true,
			wantSub: "server.port",
		},
		{
			name:    "port too large",
			mutate:  func(c *Config) { c.Server.Port = 70000 },
			wantErr: true,
			wantSub: "server.port",
		},
		{
			name:    "read_timeout zero",
			mutate:  func(c *Config) { c.Server.ReadTimeout = 0 },
			wantErr: true,
			wantSub: "server.read_timeout",
		},
		{
			name:    "write_timeout negative",
			mutate:  func(c *Config) { c.Server.WriteTimeout = -1 },
			wantErr: true,
			wantSub: "server.write_timeout",
		},
		{
			name:    "idle_timeout zero",
			mutate:  func(c *Config) { c.Server.IdleTimeout = 0 },
			wantErr: true,
			wantSub: "server.idle_timeout",
		},
		{
			name:    "graceful_shutdown_timeout zero",
			mutate:  func(c *Config) { c.Server.GracefulShutdownTimeout = 0 },
			wantErr: true,
			wantSub: "server.graceful_shutdown_timeout",
		},
		{
			name:    "worker_pool.size zero",
			mutate:  func(c *Config) { c.WorkerPool.Size = 0 },
			wantErr: true,
			wantSub: "worker_pool.size",
		},
		{
			name:    "worker_pool.queue_size negative",
			mutate:  func(c *Config) { c.WorkerPool.QueueSize = -1 },
			wantErr: true,
			wantSub: "worker_pool.queue_size",
		},
		{
			name:    "log.level invalid (TS-07)",
			mutate:  func(c *Config) { c.Log.Level = "trace" },
			wantErr: true,
			wantSub: "log.level",
		},
		{
			name:    "log.format invalid",
			mutate:  func(c *Config) { c.Log.Format = "xml" },
			wantErr: true,
			wantSub: "log.format",
		},
		{
			name:    "metrics.path missing slash (TS-08)",
			mutate:  func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "metrics" },
			wantErr: true,
			wantSub: "metrics.path",
		},
		{
			name:   "metrics disabled allows any path",
			mutate: func(c *Config) { c.Metrics.Enabled = false; c.Metrics.Path = "metrics" },
		},
		{
			name: "rate_limiter enabled requires requests_per_second > 0",
			mutate: func(c *Config) {
				c.RateLimiter = RateLimiterConfig{Enabled: true, RequestsPerSecond: 0, Burst: 10}
			},
			wantErr: true,
			wantSub: "rate_limiter.requests_per_second",
		},
		{
			name: "rate_limiter enabled requires burst > 0",
			mutate: func(c *Config) {
				c.RateLimiter = RateLimiterConfig{Enabled: true, RequestsPerSecond: 100, Burst: 0}
			},
			wantErr: true,
			wantSub: "rate_limiter.burst",
		},
		{
			name: "rate_limiter disabled ignores zero fields",
			mutate: func(c *Config) {
				c.RateLimiter = RateLimiterConfig{Enabled: false, RequestsPerSecond: 0, Burst: 0}
			},
		},
		{
			name: "circuit_breaker enabled requires max_requests > 0",
			mutate: func(c *Config) {
				c.CircuitBreaker = CircuitBreakerConfig{
					Enabled: true, MaxRequests: 0, Interval: time.Second, Timeout: time.Second,
				}
			},
			wantErr: true,
			wantSub: "circuit_breaker.max_requests",
		},
		{
			name: "circuit_breaker enabled requires interval > 0",
			mutate: func(c *Config) {
				c.CircuitBreaker = CircuitBreakerConfig{
					Enabled: true, MaxRequests: 10, Interval: 0, Timeout: time.Second,
				}
			},
			wantErr: true,
			wantSub: "circuit_breaker.interval",
		},
		{
			name: "circuit_breaker enabled requires timeout > 0",
			mutate: func(c *Config) {
				c.CircuitBreaker = CircuitBreakerConfig{
					Enabled: true, MaxRequests: 10, Interval: time.Second, Timeout: 0,
				}
			},
			wantErr: true,
			wantSub: "circuit_breaker.timeout",
		},
		{
			name: "circuit_breaker disabled ignores zero fields",
			mutate: func(c *Config) {
				c.CircuitBreaker = CircuitBreakerConfig{Enabled: false}
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cfg *Config
			if tc.mutate != nil {
				cfg = validConfig()
				tc.mutate(cfg)
			}
			err := cfg.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate() err = nil, want error containing %q", tc.wantSub)
				}
				if !strings.Contains(err.Error(), tc.wantSub) {
					t.Errorf("Validate() err = %q, want substring %q", err.Error(), tc.wantSub)
				}
				if !strings.HasPrefix(err.Error(), "config validate:") {
					t.Errorf("Validate() err = %q, want prefix %q", err.Error(), "config validate:")
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() err = %v, want nil", err)
			}
		})
	}
}

// TestValidate_LogNormalization 은 Log.Level / Log.Format 이 대소문자·앞뒤 공백을
// 포함해도 Validate 가 수락하고, 통과 후 구조체에 정규화된 소문자 값이 저장되는지
// 확인한다. 이는 logger.New 가 엄격 매칭만 수행하도록 하는 Single Source of Truth
// 계약의 핵심이다.
func TestValidate_LogNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		level      string
		format     string
		wantLevel  string
		wantFormat string
	}{
		{"uppercase", "INFO", "JSON", "info", "json"},
		{"mixed case", "Warn", "Console", "warn", "console"},
		{"surrounding whitespace", "  debug  ", "  json  ", "debug", "json"},
		{"tab and newline", "\tinfo\n", "\tconsole\n", "info", "console"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig()
			cfg.Log.Level = tc.level
			cfg.Log.Format = tc.format

			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() err = %v, want nil", err)
			}
			if cfg.Log.Level != tc.wantLevel {
				t.Errorf("Log.Level = %q, want %q (정규화된 값으로 치환되어야 함)", cfg.Log.Level, tc.wantLevel)
			}
			if cfg.Log.Format != tc.wantFormat {
				t.Errorf("Log.Format = %q, want %q (정규화된 값으로 치환되어야 함)", cfg.Log.Format, tc.wantFormat)
			}
		})
	}
}
