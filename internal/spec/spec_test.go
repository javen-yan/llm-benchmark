package spec

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spec.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	p := writeTemp(t, `
name: test
target:
  base_url: http://127.0.0.1:9000
  model: m
engine: mock
workload:
  concurrency: 2
  requests: 10
  input_tokens: 64
  output_tokens: 16
`)
	s, err := LoadFile(p)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if !s.Workload.StreamEnabled() {
		t.Error("默认应为流式")
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := []string{
		`target: {base_url: http://x, model: m}`,                        // 缺 name
		"name: x\ntarget: {model: m}\nworkload: {requests: 1}",          // 缺 base_url
		"name: x\ntarget: {base_url: http://x, model: m}",               // 缺 workload
		"name: x\ntarget: {base_url: http://x, model: m}\nworkload: {}", // requests=0
		"name: x\ntarget: {base_url: http://x, model: m}\nworkload: {requests: 1}\nengine: nosuch",
	}
	for i, c := range cases {
		if _, err := LoadFile(writeTemp(t, c)); err == nil {
			t.Errorf("case %d 应报错但通过了", i)
		}
	}
}
