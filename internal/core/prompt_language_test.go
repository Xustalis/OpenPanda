package core

import (
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

func TestPromptLanguagePolicy_RecommendedPromptLang(t *testing.T) {
	tests := []struct {
		name       string
		policy     PromptLanguagePolicy
		wantPrompt i18n.Locale
		wantOutput i18n.Locale
	}{
		{
			name: "Scenario A: Chinese user + China model",
			policy: PromptLanguagePolicy{
				UserLocale:     i18n.ChineseSimp,
				ModelGeoRegion: providers.RegionChina,
			},
			wantPrompt: i18n.ChineseSimp,
			wantOutput: i18n.ChineseSimp,
		},
		{
			name: "Scenario B: Chinese user + Global model",
			policy: PromptLanguagePolicy{
				UserLocale:     i18n.ChineseSimp,
				ModelGeoRegion: providers.RegionGlobal,
			},
			wantPrompt: i18n.English,
			wantOutput: i18n.ChineseSimp,
		},
		{
			name: "Scenario C: English user + China model",
			policy: PromptLanguagePolicy{
				UserLocale:     i18n.English,
				ModelGeoRegion: providers.RegionChina,
			},
			wantPrompt: i18n.ChineseSimp,
			wantOutput: i18n.English,
		},

		{
			name: "Scenario D: English user + Global model",
			policy: PromptLanguagePolicy{
				UserLocale:     i18n.English,
				ModelGeoRegion: providers.RegionGlobal,
			},
			wantPrompt: i18n.English,
			wantOutput: i18n.English,
		},
		{
			name: "Force English override on Chinese model",
			policy: PromptLanguagePolicy{
				UserLocale:     i18n.ChineseSimp,
				ModelGeoRegion: providers.RegionChina,
				ForceEnglish:   true,
			},
			wantPrompt: i18n.English,
			wantOutput: i18n.English,
		},
		{
			name: "Force Chinese override on Global model",
			policy: PromptLanguagePolicy{
				UserLocale:     i18n.English,
				ModelGeoRegion: providers.RegionGlobal,
				ForceChinese:   true,
			},
			wantPrompt: i18n.ChineseSimp,
			wantOutput: i18n.ChineseSimp,
		},
		{
			name: "Japanese user + Global model",
			policy: PromptLanguagePolicy{
				UserLocale:     i18n.Japanese,
				ModelGeoRegion: providers.RegionGlobal,
			},
			wantPrompt: i18n.English,
			wantOutput: i18n.Japanese,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.RecommendedPromptLang(); got != tt.wantPrompt {
				t.Errorf("RecommendedPromptLang() = %v, want %v", got, tt.wantPrompt)
			}
			if got := tt.policy.RecommendedOutputLang(); got != tt.wantOutput {
				t.Errorf("RecommendedOutputLang() = %v, want %v", got, tt.wantOutput)
			}
		})
	}
}

func TestBuildAgentPrompt_MultiLanguage(t *testing.T) {
	c := &Core{}

	// 1. English
	promptEn, _ := buildAgentPrompt(c, "Run diagnostics", "", "diag", "", i18n.English)
	if strings.Contains(promptEn, "输出与执行要求") || strings.Contains(promptEn, "中文报告") {
		t.Errorf("English agent prompt contains Chinese instructions:\n%s", promptEn)
	}
	if !strings.Contains(promptEn, "Output & Execution Requirements") || !strings.Contains(promptEn, "structured English report") {
		t.Errorf("English agent prompt missing expected English rider:\n%s", promptEn)
	}

	// 2. Chinese
	promptZh, _ := buildAgentPrompt(c, "运行诊断", "", "diag", "", i18n.ChineseSimp)
	if !strings.Contains(promptZh, "输出与执行要求") || !strings.Contains(promptZh, "中文报告") {
		t.Errorf("Chinese agent prompt missing expected Chinese rider:\n%s", promptZh)
	}

	// 3. Scenario B: Chinese User + Global Model (English prompt instructions, Chinese report output)
	promptB, _ := buildAgentPrompt(c, "排查错误", "", "debug", "", i18n.English, i18n.ChineseSimp)
	if !strings.Contains(promptB, "Output & Execution Requirements") {
		t.Errorf("Scenario B prompt instructions should be English:\n%s", promptB)
	}
	if !strings.Contains(promptB, "Simplified Chinese report") {
		t.Errorf("Scenario B should instruct model to output Simplified Chinese report:\n%s", promptB)
	}

	// 4. Scenario C: English User + China Model (Chinese prompt instructions, English report output)
	promptC, _ := buildAgentPrompt(c, "Debug error", "", "debug", "", i18n.ChineseSimp, i18n.English)
	if !strings.Contains(promptC, "输出与执行要求") {
		t.Errorf("Scenario C prompt instructions should be Chinese:\n%s", promptC)
	}
	if !strings.Contains(promptC, "英文报告") {
		t.Errorf("Scenario C should instruct model to output English report:\n%s", promptC)
	}

	// 3. Task helper GetUserLocale
	tk := Task{
		SpecJSON: `{"target":"refactor","user_locale":"zh-CN"}`,
	}
	if got := tk.GetUserLocale(); got != i18n.ChineseSimp {
		t.Errorf("tk.GetUserLocale() = %v, want zh-CN", got)
	}

	tkEn := Task{
		UserLocale: "en",
	}
	if got := tkEn.GetUserLocale(); got != i18n.English {
		t.Errorf("tkEn.GetUserLocale() = %v, want en", got)
	}
}
