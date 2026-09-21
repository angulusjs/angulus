package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func protocolResponses(t *testing.T, input string) map[int64]Response {
	t.Helper()
	var output bytes.Buffer
	if err := Serve(strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	results := map[int64]Response{}
	for {
		var response Response
		if err := decoder.Decode(&response); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("stdout is not protocol JSON: %v", err)
		}
		if response.Version != 1 {
			t.Fatalf("bad response version: %+v", response)
		}
		results[response.ID] = response
	}
	return results
}

func TestPersistentProtocol(t *testing.T) {
	requests := `{"version":1,"id":1,"method":"parse","source":"<p>{{count()}}</p>","file":"app.html"}
{"version":1,"id":2,"method":"parse","source":"<div>","file":"bad.html"}
{"version":1,"id":3,"method":"generate","nodes":[{"kind":"text","text":"hello","start":0,"end":5}],"scopeId":"data-f-test"}
`
	responses := protocolResponses(t, requests)
	if len(responses) != 3 {
		t.Fatal(responses)
	}
	for id, response := range responses {
		if response.Error != nil || response.Result == nil {
			t.Fatalf("request %d failed: %+v", id, response)
		}
	}
	parse := responses[1].Result.(map[string]any)
	if len(parse["nodes"].([]any)) != 1 || len(parse["diagnostics"].([]any)) != 0 {
		t.Fatal(parse)
	}
	bad := responses[2].Result.(map[string]any)["diagnostics"].([]any)
	if bad[0].(map[string]any)["file"] != "bad.html" {
		t.Fatal(bad)
	}
	if !strings.Contains(responses[3].Result.(map[string]any)["code"].(string), "__f.text") {
		t.Fatal(responses[3])
	}
	mappings := responses[3].Result.(map[string]any)["mappings"].([]any)
	if len(mappings) != 1 || mappings[0].(map[string]any)["source"] != float64(0) || mappings[0].(map[string]any)["generated"].(float64) <= 0 {
		t.Fatalf("missing protocol source mappings: %+v", mappings)
	}
}

func TestProtocolErrorsAndShutdown(t *testing.T) {
	responses := protocolResponses(t, `not json
{"version":2,"id":1,"method":"parse"}
{"version":1,"id":2,"method":"unknown"}
{"version":1,"id":3,"method":"cancel","targetId":999}
{"version":1,"id":4,"method":"shutdown"}
{"version":1,"id":5,"method":"parse"}
`)
	for id, code := range map[int64]string{0: "INVALID_REQUEST", 1: "UNSUPPORTED_VERSION", 2: "UNKNOWN_METHOD"} {
		if responses[id].Error == nil || responses[id].Error.Code != code {
			t.Fatalf("request %d expected %s: %+v", id, code, responses[id])
		}
	}
	if responses[3].Result.(map[string]any)["cancelled"] != false || responses[4].Result.(map[string]any)["shutdown"] != true {
		t.Fatal(responses)
	}
	if _, ok := responses[5]; ok {
		t.Fatal("processed a request after shutdown")
	}
}

func TestGenerateDiagnosticFile(t *testing.T) {
	responses := protocolResponses(t, `{"version":1,"id":1,"method":"generate","file":"component.html","nodes":[{"kind":"element","tag":"input","start":4,"end":40,"attributes":[{"name":"[innerHTML]","value":"ctx.html","start":11,"valueStart":24}]}]}
`)
	result := responses[1].Result.(map[string]any)
	diagnostics := result["diagnostics"].([]any)
	if len(diagnostics) != 1 || diagnostics[0].(map[string]any)["file"] != "component.html" {
		t.Fatalf("generation diagnostics lost request file: %+v", diagnostics)
	}
	if result["code"] != "" {
		t.Fatal("generation with errors returned executable code")
	}
}

func TestCancellation(t *testing.T) {
	request := Request{Version: 1, ID: 1, Method: "parse", Source: strings.Repeat("x", 4*1024*1024)}
	data, _ := json.Marshal(request)
	responses := protocolResponses(t, string(data)+"\n"+`{"version":1,"id":2,"method":"cancel","targetId":1}`+"\n")
	// A cancellation that races with already completed work is a successful no-op.
	cancelled := responses[2].Result.(map[string]any)["cancelled"].(bool)
	if cancelled && (responses[1].Error == nil || responses[1].Error.Code != "CANCELLED") {
		t.Fatal(responses[1])
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, fmt.Errorf("closed output") }

func TestProtocolOutputFailure(t *testing.T) {
	err := Serve(strings.NewReader("{\"version\":1,\"id\":1,\"method\":\"parse\"}\n"), failingWriter{})
	if err == nil || !strings.Contains(err.Error(), "closed output") {
		t.Fatalf("expected output error, got %v", err)
	}
}
