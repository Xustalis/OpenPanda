// SPDX-License-Identifier: AGPL-3.0-or-later

package conversation

import "strings"

// ProposalBody recognizes the confirmation footer produced by older planning
// clients. Keep the document intact up to the final standalone operator prompt.
func ProposalBody(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	start := 0
	if gap := strings.LastIndex(text, "\n\n"); gap >= 0 {
		start = gap + 2
	}
	last := strings.TrimSpace(text[start:])
	plain := strings.ToLower(strings.NewReplacer("**", "", "__", "", "’", "'").Replace(last))
	if strings.Contains(plain, "\n") {
		return text
	}
	for _, lead := range []string{"以上只是提案", "在你确认版本之前我不会", "在你确认此版本之前我不会", "在您确认版本之前我不会", "请确认是否按此计划", "please confirm this plan", "before you confirm this version, i", "i won't modify any files until you confirm", "i will not modify any files until you confirm"} {
		if !strings.HasPrefix(plain, lead) {
			continue
		}
		body := strings.TrimSpace(text[:start])
		if i := strings.LastIndexByte(body, '\n'); i >= 0 {
			switch strings.TrimSpace(body[i+1:]) {
			case "---", "***", "___":
				body = strings.TrimSpace(body[:i])
			}
		}
		return body
	}
	return text
}
