package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func testBrowserTester(t *testing.T, baseURL string, started, cancelled <-chan struct{}) {
	t.Helper()
	if _, err := exec.LookPath("agent-browser"); err != nil {
		t.Fatal("browser verification requires installed agent-browser")
	}
	session := fmt.Sprintf("jev-test-%d", os.Getpid())
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "agent-browser", append([]string{"--session", session, "--json"}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("browser %s failed: %v\n%s", args[0], err, output)
		}
		return output
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "agent-browser", "--session", session, "close").Run(); err != nil {
			t.Errorf("close test browser: %v", err)
		}
	})
	click := func(selector string) {
		t.Helper()
		// The CLI dispatches pointer clicks without scrolling offscreen controls.
		run("scrollintoview", selector)
		run("click", selector)
	}
	check := func(script string) {
		t.Helper()
		var result struct {
			Success bool
			Data    struct{ Result bool }
		}
		raw := run("eval", script)
		if err := json.Unmarshal(raw, &result); err != nil || !result.Success || !result.Data.Result {
			t.Logf("browser errors: %s", run("errors"))
			t.Logf("browser state: %s", run("eval", "({example: document.querySelector('#example-kind')?.value, request: document.querySelector('#request')?.value, status: document.querySelector('#status')?.textContent, exampleDisabled: document.querySelector('#load-example')?.disabled})"))
			t.Fatalf("browser assertion failed: %s\n%s", script, raw)
		}
	}
	status := func(text string) {
		t.Helper()
		quoted, _ := json.Marshal(text)
		run("wait", "--fn", "document.querySelector('#status').textContent.includes("+string(quoted)+")")
	}
	run("open", baseURL+"/v0/resource/plugins/cliproxy-jev-provider/tester")
	check("document.querySelector('#api-key')?.type === 'password' && document.querySelector('#request')?.tagName === 'TEXTAREA'")
	run("fill", "#api-key", "invalid-client-key")
	click("#load-models")
	status("HTTP 401")
	check("document.querySelector('#run').disabled")
	run("fill", "#api-key", "fake-client-key")
	click("#load-models")
	status("models available")
	check("document.querySelector('#models').textContent.includes('or/jev-1.13') && !document.querySelector('#models').textContent.includes('unrelated-chat') && !document.querySelector('#models').textContent.includes('disabled/')")
	run("fill", "#request", "{invalid")
	click("#run")
	status("Invalid JSON")
	run("fill", "#request", `{"model":"unrelated-chat","state":"x","questions":{}}`)
	click("#run")
	status("available JEV alias")
	run("fill", "#request", `{"model":"or/jev-1.13","state":"x","questions":{}}`)
	click("#run")
	status("HTTP 400")
	check("document.querySelector('#response').textContent.length > 0")
	large := `{"model":"or/jev-1.13","state":{"ticket":"browser-html","value":9007199254740993},"questions":{"urgent":{"type":"noul","instructions":{"question":"Urgent?"}}}}`
	run("fill", "#request", large)
	click("#run")
	status("HTTP 200")
	check("document.querySelector('#response').textContent.includes('9007199254740993') && document.querySelector('#response').textContent.includes('<img') && !document.querySelector('#response img') && window.__jevXSS === undefined")
	for _, kind := range []string{"noul", "choice", "score", "all"} {
		run("select", "#example-kind", kind)
		click("#load-example")
		if kind != "all" {
			check("Object.values(JSON.parse(document.querySelector('#request').value).questions)[0].type === '" + kind + "'")
		}
	}
	check("Object.keys(JSON.parse(document.querySelector('#request').value).questions).length === 3 && JSON.parse(document.querySelector('#request').value).model === 'jev-1.13-free'")
	click("#run")
	status("HTTP 200")
	check("document.querySelector('#response').textContent.includes('probabilities')")
	run("fill", "#request", `{"model":"or/jev-1.13","state":"browser-error","questions":{"urgent":{"type":"noul","instructions":"Urgent?"}}}`)
	click("#run")
	status("HTTP 422")
	check("document.querySelector('#response').textContent.includes('422')")
	run("fill", "#request", `{"model":"or/jev-1.13","state":"browser-cancel","questions":{"urgent":{"type":"noul","instructions":"Urgent?"}}}`)
	click("#run")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("browser cancellation request never reached upstream")
	}
	check("document.querySelector('#run').disabled && !document.querySelector('#cancel').disabled")
	run("eval", "document.querySelector('#run').click()")
	click("#cancel")
	status("Cancelled")
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("browser cancellation did not reach upstream")
	}
	run("fill", "#api-key", "changed-client-key")
	check("document.querySelector('#run').disabled && document.querySelector('#models').children.length === 0")
	run("fill", "#api-key", "fake-client-key")
	click("#load-models")
	status("models available")
	click("#run")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("key-change cancellation request never reached upstream")
	}
	run("fill", "#api-key", "changed-client-key")
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("changing the key did not cancel the upstream request")
	}
	check("document.querySelector('#run').disabled && document.querySelector('#models').children.length === 0 && document.querySelector('#response').textContent === ''")
	run("reload")
	check("document.querySelector('#api-key').value === '' && localStorage.length === 0 && sessionStorage.length === 0")
}
