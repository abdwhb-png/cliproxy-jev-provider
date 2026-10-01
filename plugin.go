package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type jevPlugin struct {
	callHost  func(string, []byte) ([]byte, error)
	mu        sync.RWMutex
	endpoints map[string]string
}

func (p *jevPlugin) handle(method string, raw []byte) ([]byte, error) {
	result, err := p.dispatch(method, raw)
	if err != nil {
		var rpcErr *pluginabi.Error
		if !errors.As(err, &rpcErr) {
			rpcErr = pluginabi.NewError("invalid_request", "invalid plugin request", 400)
		}
		return json.Marshal(pluginabi.Envelope{Error: rpcErr})
	}
	return successEnvelope(result)
}

func (p *jevPlugin) dispatch(method string, raw []byte) (any, error) {
	if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
		var lifecycle struct {
			ConfigYAML    []byte `json:"config_yaml"`
			SchemaVersion uint32 `json:"schema_version"`
		}
		if err := json.Unmarshal(raw, &lifecycle); err != nil {
			return nil, err
		}
		if lifecycle.SchemaVersion != 0 && lifecycle.SchemaVersion < pluginabi.SchemaVersion {
			return nil, pluginabi.NewError("invalid_config", "unsupported host RPC schema", 400)
		}
		var config struct {
			Endpoints map[string]string `yaml:"endpoints"`
			Enabled   bool              `yaml:"enabled"`
			Priority  int               `yaml:"priority"`
		}
		decoder := yaml.NewDecoder(bytes.NewReader(lifecycle.ConfigYAML))
		decoder.KnownFields(true)
		if len(bytes.TrimSpace(lifecycle.ConfigYAML)) > 0 {
			if err := decoder.Decode(&config); err != nil {
				return nil, pluginabi.NewError("invalid_config", "invalid JEV plugin configuration", 400)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return nil, pluginabi.NewError("invalid_config", "JEV configuration must contain one YAML document", 400)
			}
		}
		endpoints := make(map[string]string, len(upstreams))
		for upstream, spec := range upstreams {
			endpoints[upstream] = spec.Endpoint
		}
		for upstream, endpoint := range config.Endpoints {
			_, known := upstreams[upstream]
			parsed, err := url.Parse(endpoint)
			if !known || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.Path == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return nil, pluginabi.NewError("invalid_config", "endpoints must map known JEV upstreams to absolute HTTP(S) decision URLs without embedded credentials", 400)
			}
			endpoints[upstream] = endpoint
		}
		p.mu.Lock()
		p.endpoints = endpoints
		p.mu.Unlock()
		return struct {
			SchemaVersion uint32             `json:"schema_version"`
			Metadata      pluginapi.Metadata `json:"metadata"`
			Capabilities  map[string]any     `json:"capabilities"`
		}{
			SchemaVersion: pluginabi.SchemaVersion,
			Metadata: pluginapi.Metadata{
				Name: "JEV Provider", Version: "0.1.0", Author: "abdwhb-png",
				GitHubRepository: "https://github.com/abdwhb-png/cliproxy-deployment",
				ConfigFields: []pluginapi.ConfigField{{
					Name: "endpoints", Type: pluginapi.ConfigFieldTypeObject,
					Description: "Optional absolute System One endpoint overrides for typesafe, openrouter, and zen.",
				}},
			},
			Capabilities: map[string]any{
				"auth_provider": true, "model_provider": true, "executor": true,
				"executor_model_scope":    "oauth",
				"executor_input_formats":  []string{"chat-completions"},
				"executor_output_formats": []string{"chat-completions"},
			},
		}, nil
	}
	switch method {
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return map[string]string{"identifier": "jev"}, nil
	case pluginabi.MethodAuthParse:
		var req pluginapi.AuthParseRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		return parseAuth(req)
	case pluginabi.MethodModelStatic:
		return pluginapi.ModelResponse{Provider: "jev"}, nil
	case pluginabi.MethodModelForAuth:
		var req pluginapi.AuthModelRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		return modelsForAuth(req)
	case pluginabi.MethodAuthRefresh:
		var req pluginapi.AuthRefreshRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		if req.AuthID == "" || req.AuthProvider != "jev" {
			return nil, pluginabi.NewError("invalid_auth", "refresh requires JEV auth", 503)
		}
		if _, err := readCredential(req.StorageJSON); err != nil {
			return nil, err
		}
		return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: "jev", ID: req.AuthID, StorageJSON: req.StorageJSON, Metadata: req.Metadata, Attributes: req.Attributes}}, nil
	case pluginabi.MethodExecutorExecuteStream:
		return nil, pluginabi.NewError("unsupported_stream", "JEV does not support streaming", 400)
	case pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll, pluginabi.MethodExecutorCountTokens, pluginabi.MethodExecutorHTTPRequest:
		return nil, pluginabi.NewError("unsupported_operation", "JEV uses environment-key auth and native decision execution only", 501)
	case pluginabi.MethodExecutorExecute:
		var req executorRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		return p.execute(req)
	case pluginabi.MethodPluginShutdown, pluginabi.MethodPluginQuiesce:
		return struct{}{}, nil
	default:
		return nil, pluginabi.NewError("unknown_method", "unknown method: "+method, 400)
	}
}

func successEnvelope(result any) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: raw})
}

type executorRequest struct {
	pluginapi.ExecutorRequest
	HostCallbackID string `json:"host_callback_id"`
}

func (p *jevPlugin) execute(req executorRequest) (pluginapi.ExecutorResponse, error) {
	if req.AuthID == "" || req.AuthProvider != "jev" || req.HostCallbackID == "" {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("invalid_auth", "execution requires selected JEV auth and host callback context", 503)
	}
	if req.Stream || req.Alt != "" || (req.Format != "openai" && req.Format != "chat-completions") {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("unsupported_protocol", "JEV only supports non-streaming decisions on /v1/chat/completions", 400)
	}
	if path, _ := req.Metadata["request_path"].(string); path != "" && path != "/v1/chat/completions" {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("unsupported_protocol", "JEV decisions require /v1/chat/completions", 400)
	}
	auth, err := readCredential(req.StorageJSON)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	headers, err := auth.headers()
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	nativeModel := ""
	for _, model := range upstreams[auth.Upstream].Models {
		if model.ID == req.Model {
			nativeModel = model.Native
			break
		}
	}
	if nativeModel == "" {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("invalid_model", "selected JEV credential does not support this model", 400)
	}
	payload, questions, err := readDecision(req.Payload)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	delete(payload, "stream")
	payload["model"], _ = json.Marshal(nativeModel)
	body, err := json.Marshal(payload)
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	p.mu.RLock()
	endpoint := p.endpoints[auth.Upstream]
	p.mu.RUnlock()
	if endpoint == "" {
		endpoint = upstreams[auth.Upstream].Endpoint
	}
	request, err := json.Marshal(struct {
		HostCallbackID string                `json:"host_callback_id"`
		Request        pluginapi.HTTPRequest `json:"request"`
	}{req.HostCallbackID, pluginapi.HTTPRequest{Method: http.MethodPost, URL: endpoint, Headers: headers, Body: body}})
	if err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	if p.callHost == nil {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("host_unavailable", "host HTTP callback unavailable", 503)
	}
	rawResponse, err := p.callHost(pluginabi.MethodHostHTTPDo, request)
	if err != nil {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("transport_error", "JEV host HTTP request failed", 502)
	}
	var envelope pluginabi.Envelope
	if err := json.Unmarshal(rawResponse, &envelope); err != nil || !envelope.OK {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("transport_error", "JEV host HTTP request failed", 502)
	}
	var response pluginapi.HTTPResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("transport_error", "invalid host HTTP response", 502)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode < 100 || response.StatusCode > 599 {
			return pluginapi.ExecutorResponse{}, pluginabi.NewError("transport_error", "invalid host HTTP status", 502)
		}
		return pluginapi.ExecutorResponse{}, pluginabi.NewError("upstream_error", fmt.Sprintf("JEV %s returned HTTP %d", auth.Upstream, response.StatusCode), response.StatusCode)
	}
	if err := validateDecisionResponse(response.Body, questions); err != nil {
		return pluginapi.ExecutorResponse{}, err
	}
	return pluginapi.ExecutorResponse{Payload: response.Body, Headers: response.Headers}, nil
}

var activePlugin = &jevPlugin{callHost: callNativeHost}
