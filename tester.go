package main

import (
	"embed"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//go:embed web/tester.html web/tester.js web/tester.css
var testerFiles embed.FS

var testerResources = []struct{ path, file, contentType, menu string }{
	{"/tester", "web/tester.html", "text/html; charset=utf-8", "JEV Decision Tester"},
	{"/tester.js", "web/tester.js", "text/javascript; charset=utf-8", ""},
	{"/tester.css", "web/tester.css", "text/css; charset=utf-8", ""},
}

func (p *jevPlugin) registerTesterResources(req pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	if req.ResourceBasePath == "" {
		return pluginapi.ManagementRegistrationResponse{}, pluginabi.NewError("invalid_request", "host resource base path is required", 400)
	}
	resources := make([]pluginapi.ResourceRoute, 0, len(testerResources))
	for _, asset := range testerResources {
		resources = append(resources, pluginapi.ResourceRoute{Path: asset.path, Menu: asset.menu})
	}
	p.mu.Lock()
	p.resourceBase = req.ResourceBasePath
	p.mu.Unlock()
	return pluginapi.ManagementRegistrationResponse{Resources: resources}, nil
}

func (p *jevPlugin) serveTesterResource(req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	if req.Method != http.MethodGet {
		return pluginapi.ManagementResponse{StatusCode: http.StatusMethodNotAllowed}, nil
	}
	p.mu.RLock()
	base := p.resourceBase
	p.mu.RUnlock()
	for _, asset := range testerResources {
		if base == "" || req.Path != base+asset.path {
			continue
		}
		body, err := testerFiles.ReadFile(asset.file)
		if err != nil {
			return pluginapi.ManagementResponse{}, pluginabi.NewError("asset_unavailable", "JEV tester asset unavailable", 500)
		}
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type": {asset.contentType}, "Cache-Control": {"no-store"},
				"X-Content-Type-Options":  {"nosniff"},
				"Content-Security-Policy": {"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"},
			},
			Body: body,
		}, nil
	}
	return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound}, nil
}
