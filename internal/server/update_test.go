package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewerVersion 语义比较矩阵：主/次/修各段、dev、缺省、非法输入。
func TestNewerVersion(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.1.8", "v0.1.9", true},
		{"v0.1.8", "v0.2.0", true},
		{"v0.1.8", "v1.0.0", true},
		{"v0.1.9", "v0.1.9", false},
		{"v0.2.0", "v0.1.9", false},
		{"v0.1.8", "v0.1.7", false},
		{"dev", "v0.1.8", true},      // 开发构建恒提示
		{"", "v0.1.8", true},         // 空本地版本
		{"v0.1.8", "", false},        // 空远端不提醒
		{"v0.1.8", "unknown", false}, // 解析失败宁可不提醒
		{"garbage", "v0.1.8", false},
	}
	for _, c := range cases {
		if got := newerVersion(c.current, c.latest); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

// TestFetchLatestRelease 双源 JSON 同构解析：tag/html_url 提取、缺
// html_url 时回退页面、非 200/坏体报错。
func TestFetchLatestRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.9","html_url":"https://example.com/rel"}`))
		case "/no-url":
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.9"}`))
		case "/bad":
			_, _ = w.Write([]byte(`{"tag_name":""}`))
		case "/err":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tag, url, err := fetchLatestRelease(srv.Client(), srv.URL+"/ok")
	if err != nil || tag != "v0.1.9" || url != "https://example.com/rel" {
		t.Errorf("ok: tag=%q url=%q err=%v", tag, url, err)
	}
	tag, url, err = fetchLatestRelease(srv.Client(), srv.URL+"/no-url")
	if err != nil || tag != "v0.1.9" || url == "" {
		t.Errorf("no-url fallback: tag=%q url=%q err=%v", tag, url, err)
	}
	if _, _, err := fetchLatestRelease(srv.Client(), srv.URL+"/bad"); err == nil {
		t.Error("empty tag accepted")
	}
	if _, _, err := fetchLatestRelease(srv.Client(), srv.URL+"/err"); err == nil {
		t.Error("403 accepted")
	}
}

// TestUpdateCheckSourceFallback 双源回退：第一源 404 时落第二源并
// 记录 source 名；全挂时保持零值 latest。
func TestUpdateCheckSourceFallback(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.9","html_url":"https://example.com/rel"}`))
	}))
	defer ok.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer dead.Close()

	orig := updateSources
	defer func() { updateSources = orig }()

	updateSources = []string{dead.URL + "/latest", ok.URL + "/latest"}
	u := &updateChecker{client: ok.Client()}
	u.check("v0.1.8")
	info := u.snapshot()
	if info.Latest != "v0.1.9" || !info.UpdateAvailable || info.Source != "github" {
		// sourceName 对 httptest URL 判 github（非 gitcode 即 github）。
		t.Errorf("fallback: %+v", info)
	}

	updateSources = []string{dead.URL + "/latest"}
	u2 := &updateChecker{client: ok.Client()}
	u2.check("v0.1.8")
	info2 := u2.snapshot()
	if info2.Latest != "" || info2.UpdateAvailable || info2.Source != "" {
		t.Errorf("all-dead should stay zero: %+v", info2)
	}
}
