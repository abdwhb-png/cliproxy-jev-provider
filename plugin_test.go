package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRegisterDeclaresProviderAndBrowserResourcesWithoutRouting(t *testing.T) {
	p := &jevPlugin{}
	raw, err := p.handle(pluginabi.MethodPluginRegister, []byte(`{"schema_version":6}`))
	if err != nil {
		t.Fatal(err)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		t.Fatalf("registration failed: %v", envelope.Error)
	}
	var registration struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  map[string]any     `json:"capabilities"`
	}
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if registration.SchemaVersion != pluginabi.SchemaVersion {
		t.Fatalf("schema version = %d", registration.SchemaVersion)
	}
	if registration.Metadata.GitHubRepository != "https://github.com/abdwhb-png/cliproxy-jev-provider" {
		t.Errorf("plugin repository = %q", registration.Metadata.GitHubRepository)
	}
	for _, capability := range []string{"auth_provider", "model_provider", "executor", "management_api"} {
		if registration.Capabilities[capability] != true {
			t.Errorf("missing capability %s", capability)
		}
	}
	for _, capability := range []string{"model_router", "scheduler", "model_registrar", "usage_plugin"} {
		if registration.Capabilities[capability] == true {
			t.Errorf("unexpected capability %s", capability)
		}
	}
	if registration.Capabilities["executor_model_scope"] != "oauth" {
		t.Errorf("executor must serve credential-bound models")
	}
}

func TestExecutorUsesOnlySelectedAuthAndPreservesNativeDecisions(t *testing.T) {
	t.Setenv("JEV_TEST_KEY", "fake-key")
	t.Setenv("JEV_TEST_SESSION", "fake-session")
	for _, test := range []struct{ upstream, model, native, endpoint string }{
		{"typesafe", "jev-1.13", "jev-1.13.0", "https://api.typesafe.ai/v1/systemone"},
		{"openrouter", "jev-1.13", "typesafe/jev-1.13", "https://openrouter.ai/api/v1/systemone"},
		{"zen", "jev-1.13", "jev-1.13", "https://opencode.ai/zen/v1/systemone"},
		{"zen", "jev-1.13-free", "jev-1.13-free", "https://opencode.ai/zen/v1/systemone"},
	} {
		t.Run(test.upstream+"/"+test.model, func(t *testing.T) {
			calls := 0
			payload := []byte(`{"model":"` + test.model + `","state":[{"value":9007199254740993}],"questions":{"yes":{"type":"noul","instructions":{"question":"Urgent?"},"criteria":{"true":["late"],"false":"on time"}},"pick":{"type":"choice","instructions":["Pick"],"criteria":{"a":null,"b":{"description":"other"}}},"rate":{"type":"score","instructions":"Rate","criteria":["low",{"level":"high"}]}}}`)
			response := []byte(`{"id":"decision-1","provider":"TypeSafe","model":"versioned-model","answers":{"yes":{"type":"noul","noul":0.9},"pick":{"type":"choice","choice":"a","probabilities":{"a":0.8,"b":0.2},"confidence":0.7},"rate":{"type":"score","score":0.2,"probabilities":{"0":0.8,"1":0.2},"confidence":0.7}},"usage":{"input_tokens":12,"output_tokens":3,"cost":0.0001}}`)
			p := &jevPlugin{callHost: func(method string, raw []byte) ([]byte, error) {
				calls++
				if method != pluginabi.MethodHostHTTPDo {
					t.Fatalf("unexpected callback %s", method)
				}
				var request struct {
					HostCallbackID string                `json:"host_callback_id"`
					Request        pluginapi.HTTPRequest `json:"request"`
				}
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Fatal(err)
				}
				if request.HostCallbackID != "selected-context" || request.Request.Method != "POST" || request.Request.URL != test.endpoint {
					t.Fatalf("wrong host request context or destination: %+v", request)
				}
				if request.Request.Headers.Get("Authorization") != "Bearer fake-key" {
					t.Error("wrong selected credential")
				}
				if request.Request.Headers.Get("X-Opencode-Session") != "fake-session" {
					t.Error("missing credential header reference")
				}
				var sent, original map[string]json.RawMessage
				_ = json.Unmarshal(request.Request.Body, &sent)
				_ = json.Unmarshal(payload, &original)
				if string(sent["model"]) != `"`+test.native+`"` {
					t.Error("incorrect native model")
				}
				for _, field := range []string{"state", "questions"} {
					if !bytes.Equal(sent[field], original[field]) {
						t.Errorf("altered %s", field)
					}
				}
				return successEnvelope(pluginapi.HTTPResponse{StatusCode: 200, Body: response, Headers: http.Header{"Content-Type": {"application/json"}}})
			}}
			auth, _ := json.Marshal(map[string]any{"type": "jev", "upstream": test.upstream, "api_key_env": "JEV_TEST_KEY", "headers_env": map[string]string{"x-opencode-session": "JEV_TEST_SESSION"}})
			var result pluginapi.ExecutorResponse
			err := rpcResult(t, p, pluginabi.MethodExecutorExecute, struct {
				pluginapi.ExecutorRequest
				HostCallbackID string `json:"host_callback_id"`
			}{pluginapi.ExecutorRequest{AuthID: "chosen", AuthProvider: "jev", Model: test.model, Format: "openai", SourceFormat: "openai", Payload: payload, StorageJSON: auth, Headers: http.Header{"Authorization": {"Bearer client-key"}}, Metadata: map[string]any{"request_path": "/v1/chat/completions"}}, "selected-context"}, &result)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !bytes.Equal(result.Payload, response) {
				t.Fatal("native result changed or executor repeated upstream call")
			}
		})
	}
}

func rpcResult(t *testing.T, p *jevPlugin, method string, input, result any) *pluginabi.Error {
	t.Helper()
	request, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.handle(method, request)
	if err != nil {
		t.Fatal(err)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK {
		return envelope.Error
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			t.Fatal(err)
		}
	}
	return nil
}

func TestAuthAndModelsAreBoundToEnvironmentCredentials(t *testing.T) {
	t.Setenv("JEV_TEST_KEY", "fake-key")
	p := &jevPlugin{}
	for _, test := range []struct {
		upstream, prefix string
		models           []string
	}{
		{"typesafe", "typesafe", []string{"jev-1.13"}},
		{"openrouter", "or", []string{"jev-1.13"}},
		{"zen", "ocz", []string{"jev-1.13", "jev-1.13-free"}},
	} {
		t.Run(test.upstream, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{
				"type": "jev", "upstream": test.upstream, "api_key_env": "JEV_TEST_KEY",
				"prefix": test.prefix, "priority": 7, "weight": 2,
			})
			var auth pluginapi.AuthParseResponse
			err := rpcResult(t, p, pluginabi.MethodAuthParse, pluginapi.AuthParseRequest{
				Provider: "jev", FileName: "jev-" + test.upstream + ".json", RawJSON: raw,
			}, &auth)
			if err != nil {
				t.Fatal(err)
			}
			if !auth.Handled || auth.Auth.Provider != "jev" || auth.Auth.Prefix != test.prefix {
				t.Fatalf("incorrect auth binding: %+v", auth)
			}
			if string(auth.Auth.StorageJSON) != string(raw) {
				t.Fatal("changed persisted credential references")
			}
			if auth.Auth.Attributes["base_url"] != "" || auth.Auth.Attributes["api_key"] != "" {
				t.Fatal("auth exposes native compatibility attributes or a resolved secret")
			}
			var models pluginapi.ModelResponse
			err = rpcResult(t, p, pluginabi.MethodModelForAuth, pluginapi.AuthModelRequest{
				AuthID: "selected", AuthProvider: "jev", StorageJSON: auth.Auth.StorageJSON,
			}, &models)
			if err != nil {
				t.Fatal(err)
			}
			if len(models.Models) != len(test.models) {
				t.Fatalf("models = %+v", models.Models)
			}
			for i, expected := range test.models {
				if models.Models[i].ID != expected {
					t.Errorf("model = %s, want %s", models.Models[i].ID, expected)
				}
			}
		})
	}
	var unrelated pluginapi.AuthParseResponse
	if err := rpcResult(t, p, pluginabi.MethodAuthParse, pluginapi.AuthParseRequest{
		Provider: "codex", RawJSON: []byte(`{"type":"codex"}`),
	}, &unrelated); err != nil || unrelated.Handled {
		t.Fatal("plugin must decline other credential providers")
	}
	var static pluginapi.ModelResponse
	if err := rpcResult(t, p, pluginabi.MethodModelStatic, pluginapi.StaticModelRequest{}, &static); err != nil || len(static.Models) != 0 {
		t.Fatal("plugin must not advertise unbound static models")
	}
}

func TestMissingEnvironmentKeyDoesNotAdvertiseModels(t *testing.T) {
	t.Setenv("JEV_TEST_MISSING", "")
	p := &jevPlugin{}
	var result pluginapi.ModelResponse
	err := rpcResult(t, p, pluginabi.MethodModelForAuth, pluginapi.AuthModelRequest{
		AuthID: "missing", AuthProvider: "jev",
		StorageJSON: []byte(`{"type":"jev","upstream":"typesafe","api_key_env":"JEV_TEST_MISSING"}`),
	}, &result)
	if err == nil || err.HTTPStatus != 503 || len(result.Models) != 0 {
		t.Fatalf("missing key must fail discovery explicitly: %v, %+v", err, result)
	}
}

func TestMalformedDecisionsNeverReachUpstream(t *testing.T) {
	t.Setenv("JEV_TEST_KEY", "fake-key")
	base := pluginapi.ExecutorRequest{
		AuthID: "chosen", AuthProvider: "jev", Model: "jev-1.13", Format: "openai", SourceFormat: "openai",
		StorageJSON: []byte(`{"type":"jev","upstream":"typesafe","api_key_env":"JEV_TEST_KEY"}`),
		Metadata:    map[string]any{"request_path": "/v1/chat/completions"},
	}
	for _, test := range []struct{ name, body string }{
		{"chat", `{"messages":[{"role":"user","content":"hello"}]}`},
		{"missing state", `{"questions":{"q":{"type":"noul","instructions":"?"}}}`},
		{"numeric state", `{"state":2,"questions":{"q":{"type":"noul","instructions":"?"}}}`},
		{"null state", `{"state":null,"questions":{"q":{"type":"noul","instructions":"?"}}}`},
		{"empty questions", `{"state":"x","questions":{}}`},
		{"unknown type", `{"state":"x","questions":{"q":{"type":"chat","instructions":"?"}}}`},
		{"null instructions", `{"state":"x","questions":{"q":{"type":"noul","instructions":null}}}`},
		{"invalid noul criteria", `{"state":"x","questions":{"q":{"type":"noul","instructions":"?","criteria":{"yes":"yes"}}}}`},
		{"choice requires criteria", `{"state":"x","questions":{"q":{"type":"choice","instructions":"?"}}}`},
		{"choice criteria value", `{"state":"x","questions":{"q":{"type":"choice","instructions":"?","criteria":{"a":42}}}}`},
		{"score too short", `{"state":"x","questions":{"q":{"type":"score","instructions":"?","criteria":["one"]}}}`},
		{"score criteria null", `{"state":"x","questions":{"q":{"type":"score","instructions":"?","criteria":["one",null]}}}`},
		{"stream", `{"state":"x","questions":{"q":{"type":"noul","instructions":"?"}},"stream":true}`},
		{"tools", `{"state":"x","questions":{"q":{"type":"noul","instructions":"?"}},"tools":[]}`},
		{"duplicate key", `{"state":"first","state":"second","questions":{"q":{"type":"noul","instructions":"?"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &jevPlugin{callHost: func(string, []byte) ([]byte, error) { t.Fatal("invalid decision reached upstream"); return nil, nil }}
			req := base
			req.Payload = []byte(test.body)
			err := rpcResult(t, p, pluginabi.MethodExecutorExecute, executorRequest{req, "ctx"}, nil)
			if err == nil || err.HTTPStatus != 400 {
				t.Fatalf("invalid decision error = %v", err)
			}
		})
	}
	for _, path := range []string{"/v1/responses", "/v1/messages"} {
		p := &jevPlugin{callHost: func(string, []byte) ([]byte, error) { t.Fatal("unsupported path reached upstream"); return nil, nil }}
		req := base
		req.Metadata = map[string]any{"request_path": path}
		req.Payload = []byte(`{"state":"x","questions":{"q":{"type":"noul","instructions":"?"}}}`)
		if err := rpcResult(t, p, pluginabi.MethodExecutorExecute, executorRequest{req, "ctx"}, nil); err == nil || err.HTTPStatus != 400 {
			t.Fatalf("unsupported route = %v", err)
		}
	}
}

func TestExecutorRejectsMissingSelectedAuth(t *testing.T) {
	p := &jevPlugin{callHost: func(string, []byte) ([]byte, error) { t.Fatal("missing auth reached upstream"); return nil, nil }}
	for _, req := range []executorRequest{{}, {ExecutorRequest: pluginapi.ExecutorRequest{AuthID: "chosen", AuthProvider: "openrouter"}, HostCallbackID: "ctx"}} {
		if err := rpcResult(t, p, pluginabi.MethodExecutorExecute, req, nil); err == nil || err.HTTPStatus != 503 {
			t.Fatalf("auth binding error = %v", err)
		}
	}
}

func TestReconfigureValidatesEndpointsAndKeepsLastValidSnapshot(t *testing.T) {
	t.Setenv("JEV_TEST_KEY", "fake-key")
	p := &jevPlugin{}
	var lifecycle = struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}{[]byte("endpoints:\n  typesafe: http://127.0.0.1:8080/v1/systemone\n"), 6}
	if err := rpcResult(t, p, pluginabi.MethodPluginReconfigure, lifecycle, nil); err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{
		"endpoints:\n  typesafe: not-a-url\n",
		"endpoints:\n  unknown: https://example.com/v1/systemone\n",
		"endpointz: {}\n",
		"endpoints:\n  typesafe: https://user:secret@example.com/v1/systemone\n",
		"endpoints: {}\n---\nendpointz: {}\n",
	} {
		lifecycle.ConfigYAML = []byte(config)
		if err := rpcResult(t, p, pluginabi.MethodPluginReconfigure, lifecycle, nil); err == nil || err.HTTPStatus != 400 {
			t.Fatalf("invalid config accepted: %v", err)
		}
	}
	p.callHost = func(_ string, raw []byte) ([]byte, error) {
		var request struct {
			Request pluginapi.HTTPRequest `json:"request"`
		}
		_ = json.Unmarshal(raw, &request)
		if request.Request.URL != "http://127.0.0.1:8080/v1/systemone" {
			t.Error("rejected config replaced valid endpoint")
		}
		return successEnvelope(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`)})
	}
	if err := rpcResult(t, p, pluginabi.MethodExecutorExecute, validExecutorRequest(), nil); err != nil {
		t.Fatal(err)
	}
}

func validExecutorRequest() executorRequest {
	return executorRequest{pluginapi.ExecutorRequest{
		AuthID: "chosen", AuthProvider: "jev", Model: "jev-1.13", Format: "openai", SourceFormat: "openai",
		StorageJSON: []byte(`{"type":"jev","upstream":"typesafe","api_key_env":"JEV_TEST_KEY"}`),
		Payload:     []byte(`{"model":"jev-1.13","state":"example","questions":{"q":{"type":"noul","instructions":"?"}}}`),
		Metadata:    map[string]any{"request_path": "/v1/chat/completions"},
	}, "ctx"}
}

func TestInvalidUpstreamSuccessFailsWithoutFabricatingAnswers(t *testing.T) {
	t.Setenv("JEV_TEST_KEY", "fake-key")
	for _, body := range []string{
		`not json`,
		`{"model":"jev","choices":[]}`,
		`{"model":"jev","answers":{},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"jev","answers":{"q":{"type":"score","score":0.3}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"jev","answers":{"q":{"type":"noul","noul":2}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"jev","answers":{"q":{"type":"noul","noul":null}},"usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"model":"jev","answers":{"q":{"type":"noul","noul":0.3}},"usage":{"input_tokens":-1,"output_tokens":1}}`,
		`{"model":"jev","answers":{"q":{"type":"noul","noul":0.3}}}`,
	} {
		p := &jevPlugin{callHost: func(string, []byte) ([]byte, error) {
			return successEnvelope(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(body)})
		}}
		var result pluginapi.ExecutorResponse
		if err := rpcResult(t, p, pluginabi.MethodExecutorExecute, validExecutorRequest(), &result); err == nil || err.HTTPStatus != 502 || len(result.Payload) != 0 {
			t.Fatalf("invalid success accepted: error=%v payload=%s", err, result.Payload)
		}
	}
}

func TestUpstreamErrorsPreserveStatusWithoutExecutorRetries(t *testing.T) {
	t.Setenv("JEV_TEST_KEY", "fake-key")
	for _, status := range []int{400, 401, 403, 422, 429, 500, 503} {
		calls := 0
		p := &jevPlugin{callHost: func(string, []byte) ([]byte, error) {
			calls++
			return successEnvelope(pluginapi.HTTPResponse{StatusCode: status, Body: []byte(`{"detail":"sensitive provider content"}`), Headers: http.Header{"Retry-After": {"60"}}})
		}}
		err := rpcResult(t, p, pluginabi.MethodExecutorExecute, validExecutorRequest(), nil)
		if err == nil || err.HTTPStatus != status || calls != 1 {
			t.Fatalf("upstream status=%d error=%v calls=%d", status, err, calls)
		}
		if strings.Contains(err.Message, "sensitive") {
			t.Fatal("raw upstream content leaked into RPC errors")
		}
	}
}

func TestStaticCredentialsRefreshWithoutResolvingSecrets(t *testing.T) {
	p := &jevPlugin{}
	raw := []byte(`{"type":"jev","upstream":"typesafe","api_key_env":"JEV_TEST_KEY"}`)
	var response pluginapi.AuthRefreshResponse
	err := rpcResult(t, p, pluginabi.MethodAuthRefresh, pluginapi.AuthRefreshRequest{AuthID: "chosen", AuthProvider: "jev", StorageJSON: raw}, &response)
	if err != nil {
		t.Fatal(err)
	}
	if response.Auth.ID != "chosen" || !bytes.Equal(response.Auth.StorageJSON, raw) {
		t.Fatal("static reference changed during refresh")
	}
	for _, method := range []string{pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll, pluginabi.MethodExecutorCountTokens, pluginabi.MethodExecutorHTTPRequest} {
		if err := rpcResult(t, p, method, struct{}{}, nil); err == nil || err.HTTPStatus != 501 {
			t.Fatalf("unsupported operation %s: %v", method, err)
		}
	}
	if err := rpcResult(t, p, pluginabi.MethodExecutorExecuteStream, struct{}{}, nil); err == nil || err.HTTPStatus != 400 {
		t.Fatal("streaming must fail explicitly")
	}
}

func TestConcurrentReconfigurationAndExecutionUseCompleteSnapshots(t *testing.T) {
	t.Setenv("JEV_TEST_KEY", "fake-key")
	p := &jevPlugin{callHost: func(_ string, raw []byte) ([]byte, error) {
		var request struct {
			Request pluginapi.HTTPRequest `json:"request"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		if request.Request.URL != "http://127.0.0.1:8001/v1/systemone" && request.Request.URL != "http://127.0.0.1:8002/v1/systemone" {
			t.Error("execution observed incomplete endpoint snapshot")
		}
		return successEnvelope(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(`{"model":"jev","answers":{"q":{"type":"noul","noul":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`)})
	}}
	configure := func(endpoint string) {
		input := struct {
			ConfigYAML []byte `json:"config_yaml"`
		}{[]byte("endpoints:\n  typesafe: " + endpoint + "\n")}
		if err := rpcResult(t, p, pluginabi.MethodPluginReconfigure, input, nil); err != nil {
			t.Error(err)
		}
	}
	configure("http://127.0.0.1:8001/v1/systemone")
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Go(func() {
			for j := 0; j < 20; j++ {
				if err := rpcResult(t, p, pluginabi.MethodExecutorExecute, validExecutorRequest(), nil); err != nil {
					t.Error(err)
				}
			}
		})
	}
	for i := 0; i < 20; i++ {
		configure("http://127.0.0.1:8002/v1/systemone")
		configure("http://127.0.0.1:8001/v1/systemone")
	}
	workers.Wait()
}
