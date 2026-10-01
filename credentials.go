package main

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type upstreamModel struct{ ID, Native string }
type upstreamSpec struct {
	Endpoint string
	Models   []upstreamModel
}

var upstreams = map[string]upstreamSpec{
	"typesafe":   {"https://api.typesafe.ai/v1/systemone", []upstreamModel{{"jev-1.13", "jev-1.13.0"}}},
	"openrouter": {"https://openrouter.ai/api/v1/systemone", []upstreamModel{{"jev-1.13", "typesafe/jev-1.13"}}},
	"zen":        {"https://opencode.ai/zen/v1/systemone", []upstreamModel{{"jev-1.13", "jev-1.13"}, {"jev-1.13-free", "jev-1.13-free"}}},
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type credential struct {
	Type       string            `json:"type"`
	Upstream   string            `json:"upstream"`
	APIKeyEnv  string            `json:"api_key_env"`
	HeadersEnv map[string]string `json:"headers_env,omitempty"`
	Prefix     string            `json:"prefix,omitempty"`
	Label      string            `json:"label,omitempty"`
	ProxyURL   string            `json:"proxy_url,omitempty"`
	Disabled   bool              `json:"disabled,omitempty"`
}

func readCredential(raw []byte) (credential, error) {
	var auth credential
	if err := json.Unmarshal(raw, &auth); err != nil {
		return auth, pluginabi.NewError("invalid_auth", "invalid JEV credential JSON", 503)
	}
	if auth.Type != "jev" {
		return auth, pluginabi.NewError("invalid_auth", "credential type must be jev", 503)
	}
	if _, ok := upstreams[auth.Upstream]; !ok {
		return auth, pluginabi.NewError("invalid_auth", "unknown JEV upstream", 503)
	}
	if !environmentName.MatchString(auth.APIKeyEnv) {
		return auth, pluginabi.NewError("invalid_auth", "invalid api_key_env reference", 503)
	}
	for name, env := range auth.HeadersEnv {
		if !environmentName.MatchString(env) || !validHeaderName(name) {
			return auth, pluginabi.NewError("invalid_auth", "invalid headers_env reference", 503)
		}
		switch strings.ToLower(name) {
		case "authorization", "content-type", "host", "content-length", "connection", "transfer-encoding", "cookie":
			return auth, pluginabi.NewError("invalid_auth", "headers_env cannot override authentication or transport headers", 503)
		}
	}
	return auth, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			continue
		}
		return false
	}
	return true
}

func (auth credential) headers() (http.Header, error) {
	key := strings.TrimSpace(os.Getenv(auth.APIKeyEnv))
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return nil, pluginabi.NewError("missing_key", "JEV key environment reference is empty or invalid: "+auth.APIKeyEnv, 503)
	}
	headers := http.Header{"Authorization": {"Bearer " + key}, "Content-Type": {"application/json"}}
	for name, env := range auth.HeadersEnv {
		value := os.Getenv(env)
		if strings.ContainsAny(value, "\r\n") {
			return nil, pluginabi.NewError("invalid_auth", "invalid JEV header environment reference: "+env, 503)
		}
		if value != "" {
			headers.Set(name, value)
		}
	}
	return headers, nil
}

func parseAuth(req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if req.Provider != "" && req.Provider != "jev" {
		return pluginapi.AuthParseResponse{}, nil
	}
	var identity struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(req.RawJSON, &identity); err != nil || identity.Type != "jev" {
		return pluginapi.AuthParseResponse{}, nil
	}
	auth, err := readCredential(req.RawJSON)
	if err != nil {
		return pluginapi.AuthParseResponse{}, err
	}
	var metadata map[string]any
	if err := json.Unmarshal(req.RawJSON, &metadata); err != nil {
		return pluginapi.AuthParseResponse{}, err
	}
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{
		Provider: "jev", FileName: req.FileName, Label: auth.Label, Prefix: auth.Prefix,
		ProxyURL: auth.ProxyURL, Disabled: auth.Disabled,
		StorageJSON: req.RawJSON, Metadata: metadata,
		Attributes: map[string]string{"jev_upstream": auth.Upstream},
	}}, nil
}

func modelsForAuth(req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	result := pluginapi.ModelResponse{Provider: "jev"}
	if req.AuthID == "" || req.AuthProvider != "jev" {
		return result, pluginabi.NewError("invalid_auth", "model discovery requires selected JEV auth", 503)
	}
	auth, err := readCredential(req.StorageJSON)
	if err != nil {
		return result, err
	}
	if auth.Disabled {
		return result, nil
	}
	if _, err := auth.headers(); err != nil {
		return result, err
	}
	for _, model := range upstreams[auth.Upstream].Models {
		result.Models = append(result.Models, pluginapi.ModelInfo{
			ID: model.ID, Name: model.ID, Object: "model", OwnedBy: "typesafe", Type: "jev",
			Description:         "JEV typed decisions; native state/questions on /v1/chat/completions, non-streaming only.",
			SupportedParameters: []string{"state", "questions"},
		})
	}
	return result, nil
}
