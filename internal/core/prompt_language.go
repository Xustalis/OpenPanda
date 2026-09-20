package core

import (
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/providers"
)

// PromptLanguagePolicy determines the prompt and output language strategy
// based on user locale, provider origin, and explicit overrides.
type PromptLanguagePolicy struct {
	UserLocale     i18n.Locale
	ModelGeoRegion providers.GeoRegion
	ForceEnglish   bool // User explicitly requested English prompts
	ForceChinese   bool // User explicitly requested Chinese prompts
}

// RecommendedPromptLang returns the recommended language for constructing prompts.
func (p PromptLanguagePolicy) RecommendedPromptLang() i18n.Locale {
	if p.ForceChinese {
		return i18n.ChineseSimp
	}
	if p.ForceEnglish {
		return i18n.English
	}

	userLoc := p.UserLocale
	if userLoc == "" {
		userLoc = i18n.English
	}

	// Scenario A: Chinese User + China Model -> Chinese prompt
	// Scenario C: English User + China Model -> Chinese prompt (China models excel with native Chinese prompt)
	if p.ModelGeoRegion == providers.RegionChina {
		return i18n.ChineseSimp
	}

	// Scenario B: Chinese User + Global Model -> English prompt (for optimal reasoning)
	// Scenario D: English User + Global Model -> English prompt
	return i18n.English
}

// RecommendedOutputLang returns the recommended language for final human-facing outputs.
// User output language strictly respects the user's locale.
func (p PromptLanguagePolicy) RecommendedOutputLang() i18n.Locale {
	if p.ForceChinese {
		return i18n.ChineseSimp
	}
	if p.ForceEnglish {
		return i18n.English
	}
	if p.UserLocale != "" {
		return p.UserLocale
	}
	return i18n.English
}
