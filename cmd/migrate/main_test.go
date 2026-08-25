package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsUnknownCommandBeforeOpeningDatabase(t *testing.T) {
	t.Chdir(t.TempDir())
	var stderr bytes.Buffer
	code := run([]string{"unknown"}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatal("run() code = 0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "未知迁移命令") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRejectsMissingDatabaseDSN(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("LLM_PROXY_GATEWAY_ENABLED", "false")
	t.Setenv("LLM_PROXY_DATABASE_DSN", "")
	var stderr bytes.Buffer
	code := run([]string{"up"}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatal("run() code = 0; want non-zero")
	}
	if !strings.Contains(stderr.String(), "数据库连接地址未配置") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
