package parser

import (
	"strings"
	"testing"
)

// TestFixtureTvPgcNonObjectResultIsTolerated: 上游 v1.7.6 的回归夹具——TV pgc playurl 的
// 顶层 result 在遗留响应里是字符串 "suee"（C# 旧代码按对象强转，直接 InvalidOperationException）。
// 本仓全程用类型断言，非对象 result 必须被当作「没有 result 节点」继续走 data/root 兜底，
// 而不是报错或 panic；这里把 5 种非对象形态（字符串/null/数字/数组/缺失）都钉进夹具回放。
func TestFixtureTvPgcNonObjectResultIsTolerated(t *testing.T) {
	cases := []struct{ fixture, shape string }{
		{"tv-pgc-string-result", `遗留字符串 "suee"`},
		{"tv-pgc-null-result", "null"},
		{"tv-pgc-number-result", "数字"},
		{"tv-pgc-array-result", "数组"},
		{"tv-pgc-missing-result", "缺失"},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			res, err := extractFixtureWithEpid(t, c.fixture, "307930")
			if err != nil {
				t.Fatalf("result 为%s 时不应报错（非对象 result 必须放行）：%v", c.shape, err)
			}
			if len(res.Clips) != 2 {
				t.Fatalf("durl 分片数 = %d, want 2", len(res.Clips))
			}
			if res.Clips[0] != "https://upos.example.com/seg1.flv" {
				t.Errorf("首个分片 = %q, want https://upos.example.com/seg1.flv", res.Clips[0])
			}
		})
	}
}

// TestFixtureNonObjectRootErrorsCleanly: 根节点不是对象（如风控/网关返回 []）时必须给可读的
// 解析错误，而不是 panic 或静默零轨道。
func TestFixtureNonObjectRootErrorsCleanly(t *testing.T) {
	_, err := extractFixtureWithEpid(t, "tv-pgc-root-array", "307930")
	if err == nil {
		t.Fatal("根节点为数组时应返回解析错误")
	}
	if !strings.Contains(err.Error(), "parse playurl JSON") {
		t.Fatalf("错误文案应指向 playurl JSON 解析，实际: %v", err)
	}
}
