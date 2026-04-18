// cmd/server 테스트는 runCore 의 초기화 실패 경로와 exit code 매핑을 검증한다.
// 정상 기동은 내부 server 패키지의 TestRun_* 에서 이미 검증되었으므로 중복 커버하지 않는다.
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRunCore_FlagParseError 는 알 수 없는 플래그 주입 시 exitCodeError 반환을 확인한다.
func TestRunCore_FlagParseError(t *testing.T) {
	t.Parallel()
	got := runCore(context.Background(), []string{"-unknown-flag"})
	if got != exitCodeError {
		t.Errorf("runCore = %d, want %d", got, exitCodeError)
	}
}

// TestRunCore_ConfigLoadFailure 는 존재하지 않는 설정 파일 경로에 대해
// exitCodeError 를 반환하는지 검증한다.
func TestRunCore_ConfigLoadFailure(t *testing.T) {
	t.Parallel()
	got := runCore(context.Background(), []string{"-config", "/no/such/path.yaml"})
	if got != exitCodeError {
		t.Errorf("runCore = %d, want %d", got, exitCodeError)
	}
}

// TestRunCore_ConfigValidateFailure 는 스키마 위반 YAML 에 대해 exitCodeError 를 반환하는지 검증한다.
func TestRunCore_ConfigValidateFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	content := []byte(`server:
  host: "127.0.0.1"
  port: 0
  read_timeout: 1s
  write_timeout: 1s
  idle_timeout: 1s
  graceful_shutdown_timeout: 1s
  read_header_timeout: 1s
  max_header_bytes: 1024
worker_pool:
  size: 1
  queue_size: 0
metrics:
  enabled: false
  path: ""
log:
  level: "info"
  format: "json"
`)
	if err := os.WriteFile(bad, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Port=0 은 Validate 에서 거부된다.
	got := runCore(context.Background(), []string{"-config", bad})
	if got != exitCodeError {
		t.Errorf("runCore = %d, want %d", got, exitCodeError)
	}
}
