// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/defense"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/skills"
	"golang.org/x/net/html"
)

// The entry model otherwise has no window onto the live web — its knowledge
// ends at the training cutoff, so "最新", "today's", or "what just shipped"
// questions get confident stale answers. web_search and web_fetch give it a
// keyless-or-keyed path out: search returns titled links, fetch reads the
// page. Provider is config-driven because there is no reliable keyless
// search API (DuckDuckGo's html/lite endpoints answer bots with a captcha);
// an unconfigured search degrades to a guidance message the model relays,
// never a silent dead end.
//
// web_fetch is the untrusted-input case for outbound HTTP: the model picks
// the URL, so the request goes through skills' SSRF guard — https only,
// public-unicast IPs only, same policy held across redirects. Search calls
// are not guarded: their endpoint comes from config (trusted input), and a
// SearXNG instance on http://192.168.x.x is a legitimate LAN deployment.

var (
	braveSearchURL  = "https://api.search.brave.com/res/v1/web/search"
	tavilySearchURL = "https://api.tavily.com/search"
	bochaSearchURL  = "https://api.bochaai.com/v1/web-search"

	// webSearchHTTPClient serves the configured search endpoints — config
	// input, so a plain client (LAN SearXNG over http must stay reachable).
	webSearchHTTPClient = &http.Client{Timeout: 15 * time.Second}
	// webFetchHTTPClient serves model-chosen URLs — the guarded public
	// client. Package vars, like the weather endpoints, so tests can point
	// them at httptest servers.
	webFetchHTTPClient = skills.PublicFetchClient()
)

const (
	defaultSearchMaxResults = 8
	maxSearchMaxResults     = 20
	searchReadLimit         = 1 << 20
	fetchReadLimit          = 2 << 20
	defaultFetchMaxChars    = 8000
	maxFetchMaxChars        = 20000
	snippetMaxChars         = 240
)

// registerWebTools wires web_search + web_fetch into the registry.
// provider "off" is the kill switch — neither tool is registered.
func registerWebTools(reg *entry.Registry, cfg *config.Config) {
	var sc config.SearchConfig
	if cfg != nil {
		sc = cfg.Search
	}
	if strings.EqualFold(strings.TrimSpace(sc.Provider), "off") {
		return
	}

	reg.Register(entry.Tool{
		Name:        "web_search",
		Description: "联网搜索网页，获取最新内容。当用户询问最新/近期新闻、版本发布、价格、赛事比分、或任何超出训练数据的时效性问题时必须调用；对拿不准的事实也用它查证。query 填搜索关键词（可含年份提高时效）。返回标题/链接/摘要，需看全文再用 web_fetch 打开链接。Search the web for up-to-date information beyond the training cutoff.",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "搜索关键词，如 'OpenAI 最新模型 2026' / 'golang 1.26 release'"},
			},
			"required": []string{"query"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			q, _ := args["query"].(string)
			if strings.TrimSpace(q) == "" {
				return "", fmt.Errorf("query 不能为空")
			}
			return runWebSearch(ctx, sc, strings.TrimSpace(q))
		},
	})

	reg.Register(entry.Tool{
		Name:        "web_fetch",
		Description: "抓取一个公网 https 网页并返回正文纯文本（超长会截断）。用于打开 web_search 返回的链接或用户给出的 URL 读取详情；不要拿它探测内网/本机服务——仅限公网地址。Fetch a public https page and return its text content.",
		Tier:        defense.TierReversible,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":       map[string]any{"type": "string", "description": "要抓取的公网 https URL"},
				"max_chars": map[string]any{"type": "number", "description": "返回正文的最大字符数（默认 8000，上限 20000）"},
			},
			"required": []string{"url"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			rawURL, _ := args["url"].(string)
			if strings.TrimSpace(rawURL) == "" {
				return "", fmt.Errorf("url 不能为空")
			}
			maxChars := defaultFetchMaxChars
			if v, ok := args["max_chars"].(float64); ok && v > 0 {
				maxChars = int(v)
				if maxChars > maxFetchMaxChars {
					maxChars = maxFetchMaxChars
				}
			}
			return runWebFetch(ctx, strings.TrimSpace(rawURL), maxChars)
		},
	})
}

// webResult is one search hit normalized across providers.
type webResult struct {
	Title   string
	URL     string
	Snippet string
	Site    string // source name when the provider supplies one
	Date    string // published/crawled date hint, kept verbatim
}

// resolveSearchProvider picks the backend from config. Explicit provider
// names win; ""/auto infers: base_url → searxng, api_key → brave; nothing
// configured resolves to "" so the call returns setup guidance.
func resolveSearchProvider(sc config.SearchConfig) string {
	p := strings.ToLower(strings.TrimSpace(sc.Provider))
	if p == "" || p == "auto" {
		switch {
		case strings.TrimSpace(sc.BaseURL) != "":
			return "searxng"
		case searchAPIKey(sc, "brave") != "":
			return "brave"
		default:
			return ""
		}
	}
	return p
}

// searchAPIKey resolves the credential for a keyed provider: config first,
// then the provider's conventional env var as a convenience.
func searchAPIKey(sc config.SearchConfig, provider string) string {
	if k := strings.TrimSpace(sc.APIKey); k != "" {
		return k
	}
	switch provider {
	case "brave":
		for _, env := range []string{"BRAVE_SEARCH_API_KEY", "BRAVE_API_KEY"} {
			if k := strings.TrimSpace(os.Getenv(env)); k != "" {
				return k
			}
		}
	case "tavily":
		return strings.TrimSpace(os.Getenv("TAVILY_API_KEY"))
	case "bocha":
		return strings.TrimSpace(os.Getenv("BOCHA_API_KEY"))
	}
	return ""
}

func searchMaxResults(sc config.SearchConfig) int {
	n := sc.MaxResults
	if n <= 0 {
		return defaultSearchMaxResults
	}
	if n > maxSearchMaxResults {
		return maxSearchMaxResults
	}
	return n
}

func runWebSearch(ctx context.Context, sc config.SearchConfig, query string) (string, error) {
	provider := resolveSearchProvider(sc)
	if provider == "" {
		return "联网搜索未配置，无法执行。请在 config.yaml 的 search 段启用：provider 可选 searxng（自建实例，配 base_url）、brave / tavily / bocha（配 api_key，或用环境变量 OPENPANDA_SEARCH_API_KEY / 各家 *_API_KEY）。配置后模型即可自主搜索。",
			nil
	}
	maxR := searchMaxResults(sc)
	var (
		results []webResult
		err     error
	)
	switch provider {
	case "searxng":
		results, err = searchSearXNG(ctx, sc.BaseURL, query, maxR)
	case "brave":
		results, err = searchBrave(ctx, searchAPIKey(sc, "brave"), query, maxR)
	case "tavily":
		results, err = searchTavily(ctx, searchAPIKey(sc, "tavily"), query, maxR)
	case "bocha":
		results, err = searchBocha(ctx, searchAPIKey(sc, "bocha"), query, maxR)
	default:
		return "", fmt.Errorf("search.provider %q 不支持（可选：auto、searxng、brave、tavily、bocha、off）", provider)
	}
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return fmt.Sprintf("搜索「%s」没有找到结果，可换个关键词重试。", query), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "搜索「%s」得到 %d 条结果：", query, len(results))
	for i, r := range results {
		fmt.Fprintf(&b, "\n%d. %s\n   %s", i+1, strings.TrimSpace(r.Title), strings.TrimSpace(r.URL))
		meta := strings.TrimSpace(r.Site)
		if d := strings.TrimSpace(r.Date); d != "" {
			if meta != "" {
				meta += " · "
			}
			meta += d
		}
		if meta != "" {
			fmt.Fprintf(&b, "  (%s)", meta)
		}
		if s := strings.TrimSpace(r.Snippet); s != "" {
			fmt.Fprintf(&b, "\n   %s", truncateRunes(s, snippetMaxChars))
		}
	}
	fmt.Fprintf(&b, "\n\n如需某条结果的全文，用 web_fetch 打开其链接。")
	return b.String(), nil
}

// --- SearXNG: GET {base}/search?q=…&format=json — the instance's settings.yml
// must list json under search.formats, otherwise it answers 403.

type searxngResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		PubDate string `json:"publishedDate"`
	} `json:"results"`
}

func searchSearXNG(ctx context.Context, base, query string, maxR int) ([]webResult, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil, fmt.Errorf("search.provider=searxng 需要 search.base_url（自建实例地址）")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/search?q="+url.QueryEscape(query)+"&format=json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := webSearchHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng 不可达：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("searxng 返回 403：实例需在 settings.yml 的 search.formats 中启用 json")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searxng 返回 %s", resp.Status)
	}
	var out searxngResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, searchReadLimit)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 searxng 结果失败：%w", err)
	}
	results := make([]webResult, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, webResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Date: r.PubDate})
		if len(results) >= maxR {
			break
		}
	}
	return results, nil
}

// --- Brave Search API: GET /res/v1/web/search with X-Subscription-Token.

type braveResponse struct {
	Web struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
			Desc  string `json:"description"`
			Age   string `json:"age"`
		} `json:"results"`
	} `json:"web"`
}

func searchBrave(ctx context.Context, key, query string, maxR int) ([]webResult, error) {
	if key == "" {
		return nil, fmt.Errorf("search.provider=brave 需要 api_key（search.api_key / OPENPANDA_SEARCH_API_KEY / BRAVE_SEARCH_API_KEY）")
	}
	u := braveSearchURL + "?q=" + url.QueryEscape(query) + "&count=" + fmt.Sprint(maxR)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	resp, err := webSearchHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave 搜索不可达：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("brave 搜索返回 %s（检查 api_key 是否有效）", resp.Status)
	}
	var out braveResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, searchReadLimit)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 brave 结果失败：%w", err)
	}
	results := make([]webResult, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		results = append(results, webResult{Title: r.Title, URL: r.URL, Snippet: r.Desc, Date: r.Age})
	}
	return results, nil
}

// --- Tavily: POST /search {api_key, query, max_results}.

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
		PubDate string `json:"published_date"`
	} `json:"results"`
}

func searchTavily(ctx context.Context, key, query string, maxR int) ([]webResult, error) {
	if key == "" {
		return nil, fmt.Errorf("search.provider=tavily 需要 api_key（search.api_key / OPENPANDA_SEARCH_API_KEY / TAVILY_API_KEY）")
	}
	body, _ := json.Marshal(map[string]any{
		"api_key":     key,
		"query":       query,
		"max_results": maxR,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilySearchURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := webSearchHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily 搜索不可达：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tavily 搜索返回 %s（检查 api_key 是否有效）", resp.Status)
	}
	var out tavilyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, searchReadLimit)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 tavily 结果失败：%w", err)
	}
	results := make([]webResult, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, webResult{Title: r.Title, URL: r.URL, Snippet: r.Content, Date: r.PubDate})
	}
	return results, nil
}

// --- Bocha（博查）: POST /v1/web-search, Bearer key — CN-friendly AI search.

type bochaPage struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Snippet   string `json:"snippet"`
	Summary   string `json:"summary"`
	SiteName  string `json:"siteName"`
	Published string `json:"datePublished"`
	Crawled   string `json:"dateLastCrawled"`
}

type bochaResponse struct {
	Data struct {
		WebPages struct {
			Value []bochaPage `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
	// Older responses put webPages at the top level.
	WebPages struct {
		Value []bochaPage `json:"value"`
	} `json:"webPages"`
}

func searchBocha(ctx context.Context, key, query string, maxR int) ([]webResult, error) {
	if key == "" {
		return nil, fmt.Errorf("search.provider=bocha 需要 api_key（search.api_key / OPENPANDA_SEARCH_API_KEY / BOCHA_API_KEY）")
	}
	body, _ := json.Marshal(map[string]any{
		"query":     query,
		"count":     maxR,
		"freshness": "noLimit",
		"summary":   true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bochaSearchURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := webSearchHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bocha 搜索不可达：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bocha 搜索返回 %s（检查 api_key 是否有效）", resp.Status)
	}
	var out bochaResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, searchReadLimit)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析 bocha 结果失败：%w", err)
	}
	values := out.Data.WebPages.Value
	if len(values) == 0 {
		values = out.WebPages.Value
	}
	results := make([]webResult, 0, len(values))
	for _, v := range values {
		snippet := v.Summary
		if snippet == "" {
			snippet = v.Snippet
		}
		date := v.Published
		if date == "" {
			date = v.Crawled
		}
		results = append(results, webResult{Title: v.Name, URL: v.URL, Snippet: snippet, Site: v.SiteName, Date: date})
	}
	return results, nil
}

// --- web_fetch: model-chosen URL through the SSRF-guarded public client.

func runWebFetch(ctx context.Context, rawURL string, maxChars int) (string, error) {
	u, err := skills.CheckPublicFetchURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("该 URL 不允许抓取（仅限公网 https 地址）：%w", err)
	}
	return fetchWebPage(ctx, u.String(), rawURL, maxChars)
}

// fetchWebPage is the transport half of web_fetch, split out so tests can
// exercise it against loopback servers the URL policy would never admit.
func fetchWebPage(ctx context.Context, target, display string, maxChars int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "OpenPanda-webfetch/1.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")
	resp, err := webFetchHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("抓取失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("抓取 %s 返回 %s", display, resp.Status)
	}
	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchReadLimit))
	if err != nil {
		return "", fmt.Errorf("读取页面失败：%w", err)
	}

	var text string
	switch {
	case strings.Contains(ctype, "html"):
		text = htmlToText(string(body))
	case strings.HasPrefix(ctype, "text/"), strings.Contains(ctype, "json"),
		strings.Contains(ctype, "xml"), ctype == "":
		text = string(body)
	default:
		return "", fmt.Errorf("不支持的内容类型 %q（仅支持网页/文本）", ctype)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Sprintf("页面 %s 没有可提取的文本内容。", display), nil
	}
	truncated := len([]rune(text)) > maxChars
	if truncated {
		text = string([]rune(text)[:maxChars])
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s 正文：\n%s", display, text)
	if truncated {
		fmt.Fprintf(&b, "\n\n…（正文超过 %d 字符已截断；可结合页面内链接或更精确的 URL 分段读取）", maxChars)
	}
	return b.String(), nil
}

// htmlToText flattens a document to its readable text: script/style/template
// content is dropped, block-level tags become newlines, and the remaining
// text is whitespace-collapsed into non-empty lines.
func htmlToText(src string) string {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return src
	}
	var b strings.Builder
	var walk func(*html.Node)
	block := func(tag string) bool {
		switch tag {
		case "p", "div", "li", "ul", "ol", "h1", "h2", "h3", "h4", "h5", "h6",
			"br", "tr", "table", "section", "article", "header", "footer",
			"nav", "aside", "main", "blockquote", "pre", "figure", "figcaption":
			return true
		}
		return false
	}
	skip := func(tag string) bool {
		switch tag {
		case "script", "style", "noscript", "template", "head", "svg", "canvas",
			"iframe", "form", "select", "button", "input":
			return true
		}
		return false
	}
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skip(n.Data) {
				return
			}
			if block(n.Data) {
				b.WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && block(n.Data) {
			b.WriteString("\n")
		}
	}
	walk(doc)

	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if t := strings.Join(strings.Fields(ln), " "); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

func truncateRunes(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max]) + "…"
}
