package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostJEVTransport(t *testing.T) {
	if testing.Short() {
		t.Skip("native host loopback test")
	}
	t.Setenv("JEV_TEST_TYPESAFE_KEY", "fake-typesafe-key")
	t.Setenv("JEV_TEST_OR_ONE", "fake-or-one")
	t.Setenv("JEV_TEST_OR_TWO", "fake-or-two")
	t.Setenv("JEV_TEST_ZEN", "fake-zen-key")
	t.Setenv("JEV_TEST_MISSING", "")
	type upstreamRequest struct {
		Payload    map[string]json.RawMessage
		Auth, Path string
	}
	observed := make(chan upstreamRequest, 128)
	nextRequest := func(t *testing.T) upstreamRequest {
		t.Helper()
		select {
		case request := <-observed:
			return request
		case <-time.After(5 * time.Second):
			t.Fatal("missing upstream request")
			return upstreamRequest{}
		}
	}
	cancelStarted, cancelObserved := make(chan struct{}), make(chan struct{})
	browserStarted, browserCancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	const nativeResponse = `{"model":"jev-1.13.0","answers":{"urgent":{"type":"noul","noul":0.9}},"usage":{"input_tokens":12,"output_tokens":3}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		observed <- upstreamRequest{payload, r.Header.Get("Authorization"), r.URL.Path}
		w.Header().Set("Content-Type", "application/json")
		if string(payload["state"]) == `"browser-cancel"` {
			browserStarted <- struct{}{}
			<-r.Context().Done()
			browserCancelled <- struct{}{}
			return
		}
		if string(payload["state"]) == `"browser-error"` {
			w.WriteHeader(422)
			return
		}
		if bytes.Contains(payload["state"], []byte(`"browser-html"`)) {
			_, _ = io.WriteString(w, strings.TrimSuffix(nativeResponse, "}")+`,"id":9007199254740993,"note":"</pre><img src=x onerror=window.__jevXSS=true>"}`)
			return
		}
		var browserQuestions map[string]json.RawMessage
		if json.Unmarshal(payload["questions"], &browserQuestions) == nil && len(browserQuestions) == 3 {
			_, _ = io.WriteString(w, `{"model":"jev-1.13","answers":{"urgent":{"type":"noul","noul":0.9},"team":{"type":"choice","choice":"billing","confidence":0.8,"probabilities":{"billing":0.8,"support":0.2}},"severity":{"type":"score","score":0.2,"confidence":0.8,"probabilities":{"0":0.8,"1":0.1,"2":0.1}}},"usage":{"input_tokens":12,"output_tokens":3}}`)
			return
		}
		if r.URL.Path == "/chat/completions" {
			_, _ = io.WriteString(w, `{"id":"mock","object":"chat.completion","model":"unrelated-chat","choices":[{"index":0,"message":{"role":"assistant","content":"normal chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
			return
		}
		if string(payload["state"]) == `"cancel"` {
			close(cancelStarted)
			<-r.Context().Done()
			close(cancelObserved)
			return
		}
		if string(payload["state"]) == `"quota"` && r.Header.Get("Authorization") == "Bearer fake-typesafe-key" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			return
		}
		if string(payload["state"]) == `"rotate"` && r.Header.Get("Authorization") == "Bearer fake-or-one" {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, nativeResponse)
	}))
	defer upstream.Close()
	temp := t.TempDir()
	var proxied atomic.Int64
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != upstream.Listener.Addr().String() || r.Method == http.MethodConnect {
			if r.URL.Host == "api.typesafe.ai:443" || r.URL.Host == "openrouter.ai:443" || r.URL.Host == "opencode.ai:443" {
				t.Errorf("unexpected external JEV endpoint: %s", r.URL.Host)
			}
			w.WriteHeader(502)
			return
		}
		proxied.Add(1)
		forward := r.Clone(r.Context())
		forward.RequestURI = ""
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		response, err := transport.RoundTrip(forward)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer guard.Close()
	pluginDir := filepath.Join(temp, "plugins")
	authDir := filepath.Join(temp, "auths")
	for _, directory := range []string{pluginDir, authDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	buildArtifact(t, ".", filepath.Join(pluginDir, "cliproxy-jev-provider.so"), "-buildmode=c-shared", ".")
	binary := os.Getenv("JEV_TEST_HOST_BINARY")
	if binary == "" {
		binary = filepath.Join(temp, "cli-proxy-api")
		buildArtifact(t, "../../CLIProxyAPI", binary, "./cmd/server")
	}
	for _, record := range []struct{ name, body string }{
		{"jev-typesafe.json", `{"type":"jev","upstream":"typesafe","api_key_env":"JEV_TEST_TYPESAFE_KEY","prefix":"typesafe","priority":100}`},
		{"jev-or-one.json", `{"type":"jev","upstream":"openrouter","api_key_env":"JEV_TEST_OR_ONE","prefix":"or","priority":50}`},
		{"jev-or-two.json", `{"type":"jev","upstream":"openrouter","api_key_env":"JEV_TEST_OR_TWO","prefix":"or","priority":40}`},
		{"jev-zen.json", `{"type":"jev","upstream":"zen","api_key_env":"JEV_TEST_ZEN","prefix":"ocz","priority":10}`},
		{"jev-disabled.json", `{"type":"jev","upstream":"zen","api_key_env":"JEV_TEST_ZEN","prefix":"disabled","priority":1000,"disabled":true}`},
		{"jev-missing.json", `{"type":"jev","upstream":"typesafe","api_key_env":"JEV_TEST_MISSING","prefix":"missing","priority":1000}`},
		{"jev-invalid.json", `{"type":"jev","upstream":"unknown","api_key_env":"JEV_TEST_ZEN","prefix":"invalid","priority":1000}`},
	} {
		writeFixture(t, filepath.Join(authDir, record.name), []byte(record.body))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	config := fmt.Sprintf(`host: "127.0.0.1"
port: %d
auth-dir: %q
api-keys: ["fake-client-key"]
remote-management:
  secret-key: "fake-management-key"
  disable-control-panel: true
routing:
  strategy: fill-first
plugins:
  enabled: true
  dir: %q
  configs:
    cliproxy-jev-provider:
      enabled: true
      endpoints:
        typesafe: %q
        openrouter: %q
        zen: %q
openai-compatibility:
  - name: mock-chat
    base-url: %q
    api-key-entries:
      - api-key: fake-chat-key
    models:
      - name: unrelated-chat
request-retry: 1
max-retry-interval: 0
usage-statistics-enabled: true
proxy-url: %q
logging-to-file: false
debug: false
`, port, authDir, pluginDir, upstream.URL+"/v1/systemone", upstream.URL+"/api/v1/systemone", upstream.URL+"/zen/v1/systemone", upstream.URL, guard.URL)
	configPath := filepath.Join(temp, "config.yaml")
	writeFixture(t, configPath, []byte(config))
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	startTestHost(t, binary, configPath, temp, baseURL, guard.URL)
	waitForModel(t, baseURL, "typesafe/jev-1.13", filepath.Join(temp, "host.log"))
	body := []byte(`{"model":"typesafe/jev-1.13","state":{"messages":["late payment"]},"questions":{"urgent":{"type":"noul","instructions":{"question":"Is this urgent?"}}}}`)
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fake-client-key")
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		logs, _ := os.ReadFile(filepath.Join(temp, "host.log"))
		t.Logf("isolated host diagnostics: %s", logs)
		t.Fatalf("decision status = %d, body = %s", response.StatusCode, result)
	}
	if string(result) != nativeResponse {
		t.Fatalf("native response changed: %s", result)
	}
	if proxied.Load() != 1 {
		t.Fatal("selected host HTTP transport did not use the configured proxy exactly once")
	}
	select {
	case observation := <-observed:
		payload := observation.Payload
		if observation.Auth != "Bearer fake-typesafe-key" || observation.Path != "/v1/systemone" {
			t.Fatal("wrong selected TypeSafe credential or endpoint")
		}
		if string(payload["model"]) != `"jev-1.13.0"` {
			t.Errorf("upstream model = %s", payload["model"])
		}
		var original map[string]json.RawMessage
		_ = json.Unmarshal(body, &original)
		for _, field := range []string{"state", "questions"} {
			if !bytes.Equal(payload[field], original[field]) {
				t.Errorf("changed decision field %s", field)
			}
		}
	default:
		t.Fatal("no upstream decision request")
	}
	for _, model := range []string{"or/jev-1.13", "ocz/jev-1.13", "ocz/jev-1.13-free", "jev-1.13"} {
		waitForModel(t, baseURL, model, filepath.Join(temp, "host.log"))
	}
	t.Run("browser resources", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodGet, baseURL+"/v8/management/plugins", nil)
		request.Header.Set("Authorization", "Bearer fake-management-key")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		catalog, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Contains(catalog, []byte("JEV Decision Tester")) {
			t.Fatalf("plugin menu missing: %d %s", response.StatusCode, catalog)
		}
		for _, asset := range []struct{ path, contentType string }{
			{"/tester", "text/html; charset=utf-8"},
			{"/tester.js", "text/javascript; charset=utf-8"},
			{"/tester.css", "text/css; charset=utf-8"},
		} {
			response, err := client.Get(baseURL + "/v0/resource/plugins/cliproxy-jev-provider" + asset.path)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != 200 || response.Header.Get("Content-Type") != asset.contentType || len(body) == 0 {
				t.Fatalf("resource %s failed: %d", asset.path, response.StatusCode)
			}
		}
	})
	t.Run("usage is accounted once", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				t.Fatal("missing host usage record")
			case <-ticker.C:
				request, _ := http.NewRequest(http.MethodGet, baseURL+"/v8/management/observability/usage/queue?count=100", nil)
				request.Header.Set("Authorization", "Bearer fake-management-key")
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				var records []struct {
					Provider  string `json:"provider"`
					AuthIndex string `json:"auth_index"`
					Tokens    struct {
						Input  int `json:"input_tokens"`
						Output int `json:"output_tokens"`
						Total  int `json:"total_tokens"`
					}
				}
				raw, _ := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode != 200 || json.Unmarshal(raw, &records) != nil {
					t.Fatalf("usage endpoint: %d %s", response.StatusCode, raw)
				}
				if len(records) == 0 {
					continue
				}
				if len(records) != 1 || records[0].Provider != "jev" || records[0].AuthIndex == "" || records[0].Tokens.Input != 12 || records[0].Tokens.Output != 3 || records[0].Tokens.Total != 15 {
					t.Fatalf("unexpected host accounting: %s", raw)
				}
				return
			}
		}
	})
	decisionBody := func(model, state string) []byte {
		return []byte(`{"model":"` + model + `","state":"` + state + `","questions":{"urgent":{"type":"noul","instructions":"Urgent?"}}}`)
	}
	for _, test := range []struct{ model, native, auth, path string }{
		{"or/jev-1.13", "typesafe/jev-1.13", "Bearer fake-or-one", "/api/v1/systemone"},
		{"ocz/jev-1.13", "jev-1.13", "Bearer fake-zen-key", "/zen/v1/systemone"},
		{"ocz/jev-1.13-free", "jev-1.13-free", "Bearer fake-zen-key", "/zen/v1/systemone"},
		{"jev-1.13", "jev-1.13.0", "Bearer fake-typesafe-key", "/v1/systemone"},
	} {
		t.Run(test.model, func(t *testing.T) {
			status, body := postTestJSON(t, client, baseURL+"/v1/chat/completions", decisionBody(test.model, "example"))
			if status != 200 || string(body) != nativeResponse {
				t.Fatalf("native response = %d %s", status, body)
			}
			request := nextRequest(t)
			if request.Auth != test.auth || request.Path != test.path || string(request.Payload["model"]) != `"`+test.native+`"` {
				t.Fatal("host selected wrong credential, eligibility, or upstream model")
			}
		})
	}
	t.Run("invalid requests make no upstream call", func(t *testing.T) {
		for _, test := range []struct{ path, body string }{
			{"/v1/chat/completions", `{"model":"jev-1.13","messages":[{"role":"user","content":"hello"}]}`},
			{"/v1/chat/completions", `{"model":"jev-1.13","state":"x","questions":{},"stream":true}`},
			{"/v1/responses", `{"model":"jev-1.13","input":"hello"}`},
			{"/v1/messages", `{"model":"jev-1.13","max_tokens":1,"messages":[{"role":"user","content":"hello"}]}`},
			{"/v1/chat/completions", string(decisionBody("missing/jev-1.13", "example"))},
			{"/v1/chat/completions", string(decisionBody("disabled/jev-1.13", "example"))},
			{"/v1/chat/completions", string(decisionBody("invalid/jev-1.13", "example"))},
		} {
			status, body := postTestJSON(t, client, baseURL+test.path, []byte(test.body))
			if status < 400 {
				t.Fatalf("invalid request accepted: %d %s", status, body)
			}
		}
		if len(observed) != 0 {
			t.Fatal("invalid request reached upstream")
		}
	})
	t.Run("unrelated chat retains native compatibility execution", func(t *testing.T) {
		status, body := postTestJSON(t, client, baseURL+"/v1/chat/completions", []byte(`{"model":"unrelated-chat","messages":[{"role":"user","content":"hello"}]}`))
		if status != 200 || !bytes.Contains(body, []byte("normal chat")) {
			t.Fatalf("ordinary chat failed: %d %s", status, body)
		}
		if request := nextRequest(t); request.Auth != "Bearer fake-chat-key" || request.Path != "/chat/completions" {
			t.Fatal("JEV displaced unrelated executor")
		}
	})
	t.Run("client cancellation reaches upstream", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(decisionBody("typesafe/jev-1.13", "cancel")))
		request.Header.Set("Authorization", "Bearer fake-client-key")
		request.Header.Set("Content-Type", "application/json")
		done := make(chan error, 1)
		go func() {
			response, err := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
			}
			done <- err
		}()
		select {
		case <-cancelStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("upstream did not start")
		}
		cancel()
		select {
		case err := <-done:
			if err == nil {
				t.Error("canceled client request succeeded")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("client did not cancel")
		}
		select {
		case <-cancelObserved:
		case <-time.After(5 * time.Second):
			t.Fatal("host callback lost client cancellation")
		}
		nextRequest(t)
	})
	t.Run("host retries alternate credentials", func(t *testing.T) {
		status, body := postTestJSON(t, client, baseURL+"/v1/chat/completions", decisionBody("or/jev-1.13", "rotate"))
		if status != 200 {
			t.Fatalf("credential failover failed: %d %s", status, body)
		}
		first, second := nextRequest(t), nextRequest(t)
		if first.Auth != "Bearer fake-or-one" || second.Auth != "Bearer fake-or-two" {
			t.Fatal("host did not rotate eligible OpenRouter credentials")
		}
	})
	t.Run("host selects eligible provider after quota cooldown", func(t *testing.T) {
		status, body := postTestJSON(t, client, baseURL+"/v1/chat/completions", decisionBody("jev-1.13", "quota"))
		if status != 200 {
			t.Fatalf("provider failover failed: %d %s", status, body)
		}
		first, second := nextRequest(t), nextRequest(t)
		if first.Auth != "Bearer fake-typesafe-key" || !strings.HasPrefix(second.Auth, "Bearer fake-or-") {
			t.Fatal("host did not select the next eligible paid provider")
		}
		status, body = postTestJSON(t, client, baseURL+"/v1/chat/completions", decisionBody("jev-1.13", "example"))
		if status != 200 {
			t.Fatalf("cooldown request failed: %d %s", status, body)
		}
		if next := nextRequest(t); next.Auth == "Bearer fake-typesafe-key" {
			t.Fatal("host ignored quota cooldown")
		}
	})
	if os.Getenv("JEV_TEST_BROWSER") == "1" {
		t.Run("browser tester", func(t *testing.T) {
			testBrowserTester(t, baseURL, browserStarted, browserCancelled)
			if len(observed) != 5 {
				t.Fatalf("expected exactly five browser upstream calls, got %d", len(observed))
			}
			first := nextRequest(t)
			if string(first.Payload["state"]) != `{"ticket":"browser-html","value":9007199254740993}` ||
				string(first.Payload["questions"]) != `{"urgent":{"type":"noul","instructions":{"question":"Urgent?"}}}` {
				t.Fatalf("browser changed native fields: %s %s", first.Payload["state"], first.Payload["questions"])
			}
			for len(observed) > 0 {
				nextRequest(t)
			}
		})
	}
}

func postTestJSON(t *testing.T, client *http.Client, endpoint string, body []byte) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fake-client-key")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, raw
}

func waitForModel(t *testing.T, baseURL, model, logPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	var body []byte
	for {
		select {
		case <-ctx.Done():
			logs, _ := os.ReadFile(logPath)
			if len(logs) > 5000 {
				logs = logs[:5000]
			}
			t.Fatalf("model %s was not registered; models=%s; diagnostics=%s", model, body, logs)
		case <-ticker.C:
			request, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/models", nil)
			request.Header.Set("Authorization", "Bearer fake-client-key")
			response, err := client.Do(request)
			if err != nil {
				continue
			}
			body, _ = io.ReadAll(response.Body)
			_ = response.Body.Close()
			var models struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &models); err != nil {
				continue
			}
			for _, candidate := range models.Data {
				if candidate.ID == model {
					return
				}
			}
		}
	}
}

func buildArtifact(t *testing.T, directory, output string, arguments ...string) {
	t.Helper()
	args := append([]string{"build", "-mod=readonly", "-o", output}, arguments...)
	command := exec.Command("go", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "CGO_ENABLED=1", "GOPROXY=off", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", directory, err, output)
	}
}

func writeFixture(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func startTestHost(t *testing.T, binary, configPath, directory, baseURL, proxyURL string) {
	t.Helper()
	logs, err := os.Create(filepath.Join(directory, "host.log"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--config", configPath, "--local-model", "--no-browser")
	command.Dir = directory
	command.Env = append(os.Environ(), "HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "NO_PROXY=127.0.0.1,localhost")
	command.Stdout, command.Stderr = logs, logs
	if err := command.Start(); err != nil {
		_ = logs.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = command.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		_ = logs.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	for {
		select {
		case <-done:
			body, _ := os.ReadFile(logs.Name())
			t.Fatalf("test host exited: %s", body)
		case <-ctx.Done():
			t.Fatal("test host health check timed out")
		case <-ticker.C:
			response, err := client.Get(baseURL + "/healthz")
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == 200 {
					return
				}
			}
		}
	}
}
