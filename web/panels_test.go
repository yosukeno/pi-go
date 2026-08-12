package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pi-go/config"
	"github.com/yosukeno/pi-go/llm"
)

// panelHarness builds a server with panels registered, fronted by a stub
// backend that echoes what it received. No run is ever started, so the
// scripted client only has to exist.
func panelHarness(t *testing.T, panels []Panel) (*httptest.Server, *httptest.Server) {
	t.Helper()
	t.Setenv("KIMI_API_KEY", "test-key")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "backend:"+r.URL.Path+"?"+r.URL.RawQuery)
	}))
	t.Cleanup(backend.Close)

	for i := range panels {
		panels[i].URL = strings.ReplaceAll(panels[i].URL, "BACKEND", backend.URL)
	}
	mgr, err := NewManager(Config{
		Cwd:         t.TempDir(),
		SessionDir:  t.TempDir(),
		Model:       "k3",
		MaxTurns:    5,
		GateTimeout: 5 * time.Second,
		RunTimeout:  time.Minute,
		IdleTimeout: time.Hour,
		NewClient: func(c config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			return &scriptedClient{model: c.Model}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)

	srv, err := NewServer(mgr, ServerOptions{Token: "test-token", Panels: panels})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv.Handler())
	t.Cleanup(front.Close)
	return front, backend
}

func TestPanelsListed(t *testing.T) {
	front, _ := panelHarness(t, []Panel{{Name: "样本库", URL: "BACKEND"}})
	resp, err := http.Get(front.URL + "/api/panels")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/panels without token: got %d, want 401 (it is an /api route)", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/api/panels", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "样本库") || !strings.Contains(string(body), "/panels/样本库/") {
		t.Fatalf("panel listing wrong: %s", body)
	}
}

func TestPanelProxyStripsPrefix(t *testing.T) {
	front, _ := panelHarness(t, []Panel{{Name: "app", URL: "BACKEND"}})

	// Content like the page itself: no token required.
	resp, err := http.Get(front.URL + "/panels/app/deep/page?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "backend:/deep/page?x=1" {
		t.Fatalf("proxy did not strip prefix/preserve query: %s", body)
	}

	// Bare name redirects to the trailing slash so relative links resolve.
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/panels/app", nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/panels/app/" {
		t.Fatalf("redirect: got %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Unknown panel is a plain 404.
	resp, err = http.Get(front.URL + "/panels/nope/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown panel: got %d, want 404", resp.StatusCode)
	}
}

func TestPanelValidation(t *testing.T) {
	for _, p := range []Panel{
		{Name: "has/slash", URL: "http://x"},
		{Name: "has=equals", URL: "http://x"},
		{Name: "", URL: "http://x"},
		{Name: "ok", URL: "not-a-url"},
		{Name: "ok", URL: "ftp://x"},
	} {
		if err := validatePanel(p); err == nil {
			t.Errorf("%+v: want invalid", p)
		}
	}
	if err := validatePanel(Panel{Name: "样本库", URL: "http://127.0.0.1:8080"}); err != nil {
		t.Errorf("valid panel rejected: %v", err)
	}
}
