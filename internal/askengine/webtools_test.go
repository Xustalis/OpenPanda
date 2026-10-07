// SPDX-License-Identifier: AGPL-3.0-or-later

package askengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
)

// clearSearchEnv blanks every env var the search resolver reads, so a dev
// machine's real keys cannot leak into the "unconfigured" expectations.
func clearSearchEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"BRAVE_SEARCH_API_KEY", "BRAVE_API_KEY", "TAVILY_API_KEY", "BOCHA_API_KEY",
	} {
		t.Setenv(k, "")
	}
}

func TestResolveSearchProvider(t *testing.T) {
	clearSearchEnv(t)
	cases := []struct {
		sc   config.SearchConfig
		want string
	}{
		{config.SearchConfig{}, ""},
		{config.SearchConfig{Provider: "auto"}, ""},
		{config.SearchConfig{BaseURL: "https://searx.example.org"}, "searxng"},
		{config.SearchConfig{APIKey: "k"}, "brave"},
		{config.SearchConfig{Provider: "tavily"}, "tavily"},
		{config.SearchConfig{Provider: "Bocha"}, "bocha"},
		{config.SearchConfig{Provider: "off", APIKey: "k"}, "off"},
	}
	for _, c := range cases {
		if got := resolveSearchProvider(c.sc); got != c.want {
			t.Errorf("resolveSearchProvider(%+v) = %q, want %q", c.sc, got, c.want)
		}
	}
}

func TestWebSearchUnconfigured(t *testing.T) {
	clearSearchEnv(t)
	got, err := runWebSearch(context.Background(), config.SearchConfig{}, "hello")
	if err != nil {
		t.Fatalf("unconfigured search should return guidance, not error: %v", err)
	}
	if !strings.Contains(got, "未配置") || !strings.Contains(got, "provider") {
		t.Fatalf("guidance should name the fix, got: %s", got)
	}
}

func TestWebSearchSearXNG(t *testing.T) {
	clearSearchEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("missing format=json in %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[
			{"title":"Alpha release","url":"https://a.example/x","content":"first hit","publishedDate":"2026-01-01"},
			{"title":"Beta","url":"https://b.example/","content":"second hit"}
		]}`)
	}))
	defer ts.Close()

	got, err := runWebSearch(context.Background(), config.SearchConfig{
		Provider: "searxng", BaseURL: ts.URL, MaxResults: 5,
	}, "openpanda")
	if err != nil {
		t.Fatalf("searxng search: %v", err)
	}
	for _, want := range []string{"Alpha release", "https://a.example/x", "first hit", "2026-01-01", "Beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
}

func TestWebSearchSearXNGForbidden(t *testing.T) {
	clearSearchEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()
	_, err := runWebSearch(context.Background(), config.SearchConfig{
		Provider: "searxng", BaseURL: ts.URL,
	}, "x")
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "json") {
		t.Fatalf("403 should hint at enabling json format, got: %v", err)
	}
}

func TestWebSearchBrave(t *testing.T) {
	clearSearchEnv(t)
	var gotToken string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"web":{"results":[
			{"title":"Brave hit","url":"https://b.example/","description":"brave desc","age":"2026-02-02"}
		]}}`)
	}))
	defer ts.Close()
	old := braveSearchURL
	braveSearchURL = ts.URL
	t.Cleanup(func() { braveSearchURL = old })

	got, err := runWebSearch(context.Background(), config.SearchConfig{
		Provider: "brave", APIKey: "sk-brave",
	}, "news")
	if err != nil {
		t.Fatalf("brave search: %v", err)
	}
	if gotToken != "sk-brave" {
		t.Fatalf("subscription token = %q", gotToken)
	}
	if !strings.Contains(got, "Brave hit") || !strings.Contains(got, "brave desc") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestWebSearchBraveEnvKey(t *testing.T) {
	clearSearchEnv(t)
	t.Setenv("BRAVE_SEARCH_API_KEY", "sk-env")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "sk-env" {
			t.Errorf("env key not used: %q", r.Header.Get("X-Subscription-Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"web":{"results":[]}}`)
	}))
	defer ts.Close()
	old := braveSearchURL
	braveSearchURL = ts.URL
	t.Cleanup(func() { braveSearchURL = old })

	got, err := runWebSearch(context.Background(), config.SearchConfig{Provider: "brave"}, "q")
	if err != nil {
		t.Fatalf("brave env-key search: %v", err)
	}
	if !strings.Contains(got, "没有找到结果") {
		t.Fatalf("empty results should say so, got: %s", got)
	}
}

func TestWebSearchTavily(t *testing.T) {
	clearSearchEnv(t)
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"Tavily hit","url":"https://t.example/","content":"tavily content"}]}`)
	}))
	defer ts.Close()
	old := tavilySearchURL
	tavilySearchURL = ts.URL
	t.Cleanup(func() { tavilySearchURL = old })

	got, err := runWebSearch(context.Background(), config.SearchConfig{
		Provider: "tavily", APIKey: "tvly-x", MaxResults: 3,
	}, "q")
	if err != nil {
		t.Fatalf("tavily search: %v", err)
	}
	if body["api_key"] != "tvly-x" {
		t.Fatalf("api_key not sent in body: %v", body)
	}
	if !strings.Contains(got, "Tavily hit") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestWebSearchBocha(t *testing.T) {
	clearSearchEnv(t)
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"webPages":{"value":[
			{"name":"博查命中","url":"https://bo.example/","snippet":"短摘要","summary":"长摘要","siteName":"示例站","datePublished":"2026-03-01"}
		]}}}`)
	}))
	defer ts.Close()
	old := bochaSearchURL
	bochaSearchURL = ts.URL
	t.Cleanup(func() { bochaSearchURL = old })

	got, err := runWebSearch(context.Background(), config.SearchConfig{
		Provider: "bocha", APIKey: "bk",
	}, "最新")
	if err != nil {
		t.Fatalf("bocha search: %v", err)
	}
	if auth != "Bearer bk" {
		t.Fatalf("authorization = %q", auth)
	}
	for _, want := range []string{"博查命中", "示例站", "2026-03-01", "长摘要"} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
}

func TestWebSearchUnknownProvider(t *testing.T) {
	clearSearchEnv(t)
	_, err := runWebSearch(context.Background(), config.SearchConfig{Provider: "altavista"}, "q")
	if err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("unknown provider should error, got: %v", err)
	}
}

func TestWebSearchMissingKey(t *testing.T) {
	clearSearchEnv(t)
	_, err := runWebSearch(context.Background(), config.SearchConfig{Provider: "tavily"}, "q")
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("missing key should error with the fix, got: %v", err)
	}
}

func TestWebToolsRegistration(t *testing.T) {
	reg := entry.NewRegistry()
	registerWebTools(reg, &config.Config{})
	if _, ok := reg.Lookup("web_search"); !ok {
		t.Fatal("web_search not registered")
	}
	if _, ok := reg.Lookup("web_fetch"); !ok {
		t.Fatal("web_fetch not registered")
	}
	reg2 := entry.NewRegistry()
	registerWebTools(reg2, &config.Config{Search: config.SearchConfig{Provider: "off"}})
	if _, ok := reg2.Lookup("web_search"); ok {
		t.Fatal("provider=off must not register web_search")
	}
	if _, ok := reg2.Lookup("web_fetch"); ok {
		t.Fatal("provider=off must not register web_fetch")
	}
}

func TestWebFetchHTML(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>t</title><script>evil()</script><style>body{color:red}</style></head>
<body><nav>导航菜单</nav><article><h1>标题一</h1><p>第一段   内容</p><p>第二段</p></article></body></html>`)
	}))
	defer ts.Close()
	old := webFetchHTTPClient
	webFetchHTTPClient = ts.Client()
	t.Cleanup(func() { webFetchHTTPClient = old })

	got, err := fetchWebPage(context.Background(), ts.URL, ts.URL, 8000)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	for _, want := range []string{"标题一", "第一段 内容", "第二段"} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted text missing %q:\n%s", want, got)
		}
	}
	for _, drop := range []string{"evil()", "color:red"} {
		if strings.Contains(got, drop) {
			t.Errorf("script/style leaked into output: %q\n%s", drop, got)
		}
	}
}

func TestWebFetchTruncates(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("字", 500))
	}))
	defer ts.Close()
	old := webFetchHTTPClient
	webFetchHTTPClient = ts.Client()
	t.Cleanup(func() { webFetchHTTPClient = old })

	got, err := fetchWebPage(context.Background(), ts.URL, ts.URL, 100)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(got, "已截断") {
		t.Fatalf("long body should carry the truncation note:\n%s", got)
	}
}

func TestWebFetchRefusesNonPublic(t *testing.T) {
	for _, u := range []string{
		"http://localhost:8080/admin",
		"http://127.0.0.1/x",
		"https://169.254.169.254/latest/meta-data",
		"http://192.168.1.1/config",
		"ftp://example.com/x",
		"not-a-url",
	} {
		if _, err := runWebFetch(context.Background(), u, 1000); err == nil {
			t.Errorf("fetch of %s should be refused", u)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><body><h1>A</h1><p>one <b>two</b> three</p><ul><li>x</li><li>y</li></ul><script>bad()</script></body></html>`
	out := htmlToText(in)
	if strings.Contains(out, "bad()") {
		t.Fatalf("script content leaked: %q", out)
	}
	if !strings.Contains(out, "one two three") || !strings.Contains(out, "x") || !strings.Contains(out, "y") {
		t.Fatalf("text extraction lost content: %q", out)
	}
}
