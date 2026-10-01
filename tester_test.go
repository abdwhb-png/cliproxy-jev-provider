package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestBrowserResourcesDeclareOneMenuAndServeOnlyStaticAssets(t *testing.T) {
	p := &jevPlugin{callHost: func(string, []byte) ([]byte, error) {
		t.Fatal("static resources must not access host credentials or execute requests")
		return nil, nil
	}}
	var registration pluginapi.ManagementRegistrationResponse
	request := pluginapi.ManagementRegistrationRequest{ResourceBasePath: "/v0/resource/plugins/cliproxy-jev-provider"}
	if err := rpcResult(t, p, pluginabi.MethodManagementRegister, request, &registration); err != nil {
		t.Fatal(err)
	}
	if len(registration.Routes) != 0 || len(registration.Resources) != 3 {
		t.Fatalf("expected three resources and no management execution routes: %+v", registration)
	}
	menus := 0
	for _, route := range registration.Resources {
		if route.Menu != "" {
			menus++
			if route.Path != "/tester" || route.Menu != "JEV Decision Tester" {
				t.Fatalf("unexpected menu: %+v", route)
			}
		}
	}
	if menus != 1 {
		t.Fatalf("expected one menu, got %d", menus)
	}
	for _, test := range []struct{ path, contentType string }{
		{"/tester", "text/html; charset=utf-8"},
		{"/tester.js", "text/javascript; charset=utf-8"},
		{"/tester.css", "text/css; charset=utf-8"},
	} {
		var response pluginapi.ManagementResponse
		if err := rpcResult(t, p, pluginabi.MethodManagementHandle, pluginapi.ManagementRequest{
			Method: "GET", Path: request.ResourceBasePath + test.path,
		}, &response); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || response.Headers.Get("Content-Type") != test.contentType || len(response.Body) == 0 {
			t.Fatalf("invalid %s response: %+v", test.path, response)
		}
		if response.Headers.Get("Cache-Control") != "no-store" || response.Headers.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("resource responses must prevent caching and content sniffing")
		}
		if test.path == "/tester" && !bytes.Contains(response.Body, []byte("JEV Decision Tester")) {
			t.Fatal("tester page missing title")
		}
	}
	for _, test := range []struct {
		method, path string
		status       int
	}{
		{"POST", request.ResourceBasePath + "/tester", http.StatusMethodNotAllowed},
		{"GET", request.ResourceBasePath + "/auths.json", http.StatusNotFound},
		{"GET", request.ResourceBasePath + "/../tester", http.StatusNotFound},
	} {
		var response pluginapi.ManagementResponse
		if err := rpcResult(t, p, pluginabi.MethodManagementHandle, pluginapi.ManagementRequest{Method: test.method, Path: test.path}, &response); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != test.status {
			t.Fatalf("%s %s status = %d", test.method, test.path, response.StatusCode)
		}
	}
}

func TestBrowserResourcesUseHostProvidedNamespace(t *testing.T) {
	p := &jevPlugin{}
	base := "/custom/resource/plugins/cliproxy-jev-provider"
	var registration pluginapi.ManagementRegistrationResponse
	if err := rpcResult(t, p, pluginabi.MethodManagementRegister, pluginapi.ManagementRegistrationRequest{
		ResourceBasePath: base,
	}, &registration); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.ManagementResponse
	if err := rpcResult(t, p, pluginabi.MethodManagementHandle, pluginapi.ManagementRequest{
		Method: "GET", Path: base + "/tester",
	}, &response); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("host-provided resource namespace returned %d", response.StatusCode)
	}
}
