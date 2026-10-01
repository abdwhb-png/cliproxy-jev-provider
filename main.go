package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
    uint32_t abi_version;
    void* host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static cliproxy_host_api stored_host;
static void store_host_api(const cliproxy_host_api* host) { stored_host = *host; }
static int call_host_api(const char* method, const uint8_t* request, size_t len, cliproxy_buffer* response) {
    if (stored_host.call == NULL) return 1;
    return stored_host.call(stored_host.host_ctx, method, request, len, response);
}
static void free_host_buffer(void* ptr, size_t len) {
    if (ptr != NULL && stored_host.free_buffer != NULL) stored_host.free_buffer(ptr, len);
}
*/
import "C"

import (
	"errors"
	"math"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || uint32(host.abi_version) != pluginabi.ABIVersion {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, length C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr, response.len = nil, 0
	if method == nil || length > math.MaxInt32 || (request == nil && length > 0) {
		raw, _ := pluginabi.NewErrorEnvelope("invalid_request", "invalid ABI request", 400)
		writeResponse(response, raw)
		return 1
	}
	var payload []byte
	if length > 0 {
		payload = C.GoBytes(unsafe.Pointer(request), C.int(length))
	}
	raw, err := activePlugin.handle(C.GoString(method), payload)
	if err != nil {
		raw, _ = pluginabi.NewErrorEnvelope("plugin_error", "JEV plugin failed to encode its response", 500)
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) { C.free(ptr) }

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if len(raw) > 0 {
		response.ptr = C.CBytes(raw)
		response.len = C.size_t(len(raw))
	}
}

func callNativeHost(method string, raw []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var request *C.uint8_t
	if len(raw) > 0 {
		request = (*C.uint8_t)(C.CBytes(raw))
		defer C.free(unsafe.Pointer(request))
	}
	var response C.cliproxy_buffer
	code := C.call_host_api(cMethod, request, C.size_t(len(raw)), &response)
	defer C.free_host_buffer(response.ptr, response.len)
	if response.len > math.MaxInt32 || response.ptr == nil {
		return nil, errors.New("invalid host callback response")
	}
	payload := C.GoBytes(response.ptr, C.int(response.len))
	if code != 0 && len(payload) == 0 {
		return nil, errors.New("host callback failed")
	}
	return payload, nil
}
