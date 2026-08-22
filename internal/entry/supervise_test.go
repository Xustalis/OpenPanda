package entry

import (
	"context"
	"strings"
	"testing"
)

func TestParseSuperviseVerdict(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantStat string
		wantFup  string
		wantErr  bool
	}{
		{"bare done", `{"status":"done","reason":"ok","followup":""}`, "done", "", false},
		{"bare continue", `{"status":"continue","reason":"half","followup":"finish the tests"}`, "continue", "finish the tests", false},
		{"fenced json", "```json\n{\"status\":\"done\",\"reason\":\"ok\"}\n```", "done", "", false},
		{"prose around", "判断如下：\n{\"status\":\"continue\",\"reason\":\"漏了\",\"followup\":\"补上\"}\n完毕", "continue", "补上", false},
		{"uppercase status", `{"status":"DONE","reason":"ok"}`, "done", "", false},
		{"invalid status", `{"status":"maybe","reason":"x"}`, "", "", true},
		{"no json", "just some prose", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := parseSuperviseVerdict(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseSuperviseVerdict(%q) = %+v, want error", tt.raw, v)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSuperviseVerdict(%q): %v", tt.raw, err)
			}
			if v.Status != tt.wantStat || v.Followup != tt.wantFup {
				t.Fatalf("parseSuperviseVerdict(%q) = %+v, want status=%q followup=%q", tt.raw, v, tt.wantStat, tt.wantFup)
			}
		})
	}
}

func TestSuperviseDone(t *testing.T) {
	c := startModelServer(t, `{"status":"done","reason":"all criteria met","followup":""}`)
	v, err := Supervise(context.Background(), c, "修复登录 bug", "已完成修复并验证")
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if v.Status != "done" {
		t.Fatalf("status = %q, want done", v.Status)
	}
}

func TestSuperviseContinue(t *testing.T) {
	c := startModelServer(t, `{"status":"continue","reason":"只改了一半","followup":"补齐单元测试"}`)
	v, err := Supervise(context.Background(), c, "实现 feature X", "改了模型层")
	if err != nil {
		t.Fatalf("supervise: %v", err)
	}
	if v.Status != "continue" || v.Followup != "补齐单元测试" {
		t.Fatalf("verdict = %+v, want continue with followup", v)
	}
}

func TestSuperviseFailOpenOnUnparsable(t *testing.T) {
	c := startModelServer(t, "抱歉，我无法判断。")
	v, err := Supervise(context.Background(), c, "task A", "agent said done")
	if err != nil {
		t.Fatalf("supervise must not error on unparsable verdict: %v", err)
	}
	if v.Status != "done" {
		t.Fatalf("status = %q, want done (fail open)", v.Status)
	}
	if !strings.Contains(v.Reason, "unparsable") {
		t.Fatalf("reason = %q, want unparsable marker", v.Reason)
	}
}