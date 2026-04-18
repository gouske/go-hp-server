// Package config 는 YAML 파일과 환경변수를 병합하여 서버 설정을
// 단일 진입점에서 로드하는 기능을 제공한다.
//
// 환경변수는 YAML 값을 항상 덮어쓰며, 매핑 규칙은 YAML 계층 경로의
// 점(`.`)을 언더스코어(`_`)로 치환한 대문자 이름이다.
// 예) server.read_timeout -> SERVER_READ_TIMEOUT
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 는 서버 전역 설정의 루트 구조체이며
// config/default.yaml 의 최상위 키와 1:1 로 매핑된다.
type Config struct {
	Server         ServerConfig         `mapstructure:"server"`
	WorkerPool     WorkerPoolConfig     `mapstructure:"worker_pool"`
	RateLimiter    RateLimiterConfig    `mapstructure:"rate_limiter"`
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker"`
	Metrics        MetricsConfig        `mapstructure:"metrics"`
	Log            LogConfig            `mapstructure:"log"`
}

// ServerConfig 는 HTTP 서버 바인딩 주소 및 타임아웃 설정이다.
//
// ReadHeaderTimeout 과 MaxHeaderBytes 는 slowloris / 헤더 폭탄 방어의
// 보안 한계값으로, 반드시 양수여야 한다. server 패키지에서는
// Option > Config > 내장 기본값 순서로 값을 결정한다.
type ServerConfig struct {
	Host                    string        `mapstructure:"host"`
	Port                    int           `mapstructure:"port"`
	ReadTimeout             time.Duration `mapstructure:"read_timeout"`
	WriteTimeout            time.Duration `mapstructure:"write_timeout"`
	IdleTimeout             time.Duration `mapstructure:"idle_timeout"`
	GracefulShutdownTimeout time.Duration `mapstructure:"graceful_shutdown_timeout"`
	ReadHeaderTimeout       time.Duration `mapstructure:"read_header_timeout"`
	MaxHeaderBytes          int           `mapstructure:"max_header_bytes"`
}

// WorkerPoolConfig 는 Worker Pool 크기 및 대기열 설정이다.
type WorkerPoolConfig struct {
	Size      int `mapstructure:"size"`
	QueueSize int `mapstructure:"queue_size"`
}

// RateLimiterConfig 는 요청 속도 제한 설정이다.
type RateLimiterConfig struct {
	Enabled           bool `mapstructure:"enabled"`
	RequestsPerSecond int  `mapstructure:"requests_per_second"`
	Burst             int  `mapstructure:"burst"`
}

// CircuitBreakerConfig 는 장애 전파 차단기(Circuit Breaker) 설정이다.
type CircuitBreakerConfig struct {
	Enabled     bool          `mapstructure:"enabled"`
	MaxRequests int           `mapstructure:"max_requests"`
	Interval    time.Duration `mapstructure:"interval"`
	Timeout     time.Duration `mapstructure:"timeout"`
}

// MetricsConfig 는 Prometheus 메트릭 노출 설정이다.
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Path    string `mapstructure:"path"`
}

// LogConfig 는 zerolog 출력 레벨 및 출력 형식을 결정한다.
// Level 은 debug|info|warn|error, Format 은 json|console 중 하나여야 한다.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Load 는 주어진 YAML 경로에서 설정을 읽어 Config 를 반환한다.
//
// 환경변수가 존재하면 동일 키의 YAML 값을 덮어쓴다. 매핑 규칙은 YAML 계층
// 경로의 점(`.`)을 언더스코어(`_`)로 치환한 대문자 이름이며,
// 예를 들어 server.read_timeout 은 SERVER_READ_TIMEOUT 으로 매핑된다.
//
// 필수 필드가 비어있거나 타입이 맞지 않으면 `config load:` 접두 에러를 반환한다.
// 에러 판별은 호출부의 책임이며, 본 함수는 panic 을 일으키지 않는다.
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config load: path is empty")
	}

	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("config load: read %q: %w", path, err)
	}

	// Viper Unmarshal 이 중첩 키의 환경변수를 정확히 인식하도록
	// YAML 에 등장한 모든 키를 명시적으로 env 바인딩한다.
	for _, key := range v.AllKeys() {
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("config load: bind env %q: %w", key, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("config load: unmarshal: %w", err)
	}
	return &cfg, nil
}

// Validate 는 로드된 Config 의 의미적 유효성을 검사한다.
// 규칙을 위반한 첫 번째 필드에 대해 `config validate:` 접두 에러를 반환한다.
//
// Log.Level / Log.Format 은 대소문자·앞뒤 공백을 허용하며, 검증을 통과한
// 값은 정규화된 소문자 형태로 c 에 저장되어 이후 소비자(logger.New 등)는
// 엄격 매칭만 수행하면 된다.
//
// 검사 규칙:
//   - Server.Host: 공백이 아님
//   - Server.Port: 1..65535
//   - Server.*Timeout: > 0 (ReadHeaderTimeout 포함)
//   - Server.MaxHeaderBytes: > 0
//   - WorkerPool.Size: > 0
//   - WorkerPool.QueueSize: >= 0
//   - RateLimiter: Enabled=true 일 때 requests_per_second>0, burst>0
//   - CircuitBreaker: Enabled=true 일 때 max_requests>0, interval>0, timeout>0
//   - Log.Level: debug|info|warn|error (대소문자·공백 허용, 정규화 후 저장)
//   - Log.Format: json|console (대소문자·공백 허용, 정규화 후 저장)
//   - Metrics.Path: Enabled=true 일 때 `/` 로 시작
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config validate: nil config")
	}
	if strings.TrimSpace(c.Server.Host) == "" {
		return errors.New("config validate: server.host must not be empty")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("config validate: server.port out of range: %d", c.Server.Port)
	}
	if c.Server.ReadTimeout <= 0 {
		return fmt.Errorf("config validate: server.read_timeout must be > 0: %s", c.Server.ReadTimeout)
	}
	if c.Server.WriteTimeout <= 0 {
		return fmt.Errorf("config validate: server.write_timeout must be > 0: %s", c.Server.WriteTimeout)
	}
	if c.Server.IdleTimeout <= 0 {
		return fmt.Errorf("config validate: server.idle_timeout must be > 0: %s", c.Server.IdleTimeout)
	}
	if c.Server.GracefulShutdownTimeout <= 0 {
		return fmt.Errorf("config validate: server.graceful_shutdown_timeout must be > 0: %s", c.Server.GracefulShutdownTimeout)
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("config validate: server.read_header_timeout must be > 0: %s", c.Server.ReadHeaderTimeout)
	}
	if c.Server.MaxHeaderBytes <= 0 {
		return fmt.Errorf("config validate: server.max_header_bytes must be > 0: %d", c.Server.MaxHeaderBytes)
	}
	if c.WorkerPool.Size <= 0 {
		return fmt.Errorf("config validate: worker_pool.size must be > 0: %d", c.WorkerPool.Size)
	}
	if c.WorkerPool.QueueSize < 0 {
		return fmt.Errorf("config validate: worker_pool.queue_size must be >= 0: %d", c.WorkerPool.QueueSize)
	}
	if c.RateLimiter.Enabled {
		if c.RateLimiter.RequestsPerSecond <= 0 {
			return fmt.Errorf("config validate: rate_limiter.requests_per_second must be > 0 when enabled: %d", c.RateLimiter.RequestsPerSecond)
		}
		if c.RateLimiter.Burst <= 0 {
			return fmt.Errorf("config validate: rate_limiter.burst must be > 0 when enabled: %d", c.RateLimiter.Burst)
		}
	}
	if c.CircuitBreaker.Enabled {
		if c.CircuitBreaker.MaxRequests <= 0 {
			return fmt.Errorf("config validate: circuit_breaker.max_requests must be > 0 when enabled: %d", c.CircuitBreaker.MaxRequests)
		}
		if c.CircuitBreaker.Interval <= 0 {
			return fmt.Errorf("config validate: circuit_breaker.interval must be > 0 when enabled: %s", c.CircuitBreaker.Interval)
		}
		if c.CircuitBreaker.Timeout <= 0 {
			return fmt.Errorf("config validate: circuit_breaker.timeout must be > 0 when enabled: %s", c.CircuitBreaker.Timeout)
		}
	}
	normLevel := strings.ToLower(strings.TrimSpace(c.Log.Level))
	if !isValidLogLevel(normLevel) {
		return fmt.Errorf("config validate: log.level invalid: %q (want debug|info|warn|error)", c.Log.Level)
	}
	normFormat := strings.ToLower(strings.TrimSpace(c.Log.Format))
	if !isValidLogFormat(normFormat) {
		return fmt.Errorf("config validate: log.format invalid: %q (want json|console)", c.Log.Format)
	}
	c.Log.Level = normLevel
	c.Log.Format = normFormat
	if c.Metrics.Enabled && !strings.HasPrefix(c.Metrics.Path, "/") {
		return fmt.Errorf("config validate: metrics.path must start with '/': %q", c.Metrics.Path)
	}
	return nil
}

// isValidLogLevel 은 LogConfig.Level 허용 집합 여부를 반환한다.
func isValidLogLevel(level string) bool {
	switch level {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}

// isValidLogFormat 은 LogConfig.Format 허용 집합 여부를 반환한다.
func isValidLogFormat(format string) bool {
	switch format {
	case "json", "console":
		return true
	}
	return false
}
