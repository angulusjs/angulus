package compiler

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

const ProtocolVersion = 1

type Request struct {
	Version    int               `json:"version"`
	ID         int64             `json:"id"`
	Method     string            `json:"method"`
	Source     string            `json:"source"`
	File       string            `json:"file"`
	Nodes      []Node            `json:"nodes"`
	ScopeID    string            `json:"scopeId"`
	Components map[string]string `json:"components"`
	TargetID   int64             `json:"targetId"`
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Version int            `json:"version"`
	ID      int64          `json:"id"`
	Result  any            `json:"result,omitempty"`
	Error   *ProtocolError `json:"error,omitempty"`
}

// Serve handles persistent NDJSON, with independent cancellable request jobs.
// All writes are serialized; output contains protocol records only.
func Serve(input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	encoder := json.NewEncoder(output)
	var writerMu, jobsMu sync.Mutex
	var workers sync.WaitGroup
	jobs := map[int64]context.CancelFunc{}
	var writeErr error
	send := func(response Response) {
		writerMu.Lock()
		defer writerMu.Unlock()
		if writeErr == nil {
			writeErr = encoder.Encode(response)
		}
	}
	fail := func(id int64, code, message string) {
		send(Response{Version: ProtocolVersion, ID: id, Error: &ProtocolError{code, message}})
	}
	stop := func() {
		jobsMu.Lock()
		for _, cancel := range jobs {
			cancel()
		}
		jobsMu.Unlock()
		workers.Wait()
	}
	for scanner.Scan() {
		var request Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			fail(request.ID, "INVALID_REQUEST", "Invalid JSON request: "+err.Error())
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &fields); err != nil || fields["id"] == nil || string(fields["id"]) == "null" {
			fail(request.ID, "INVALID_REQUEST", "Request requires a numeric id")
			continue
		}
		if request.Version != ProtocolVersion {
			fail(request.ID, "UNSUPPORTED_VERSION", fmt.Sprintf("Expected protocol version %d", ProtocolVersion))
			continue
		}
		switch request.Method {
		case "shutdown":
			stop()
			send(Response{Version: ProtocolVersion, ID: request.ID, Result: map[string]bool{"shutdown": true}})
			return writeErr
		case "cancel":
			jobsMu.Lock()
			cancel, exists := jobs[request.TargetID]
			if exists {
				cancel()
			}
			jobsMu.Unlock()
			send(Response{Version: ProtocolVersion, ID: request.ID, Result: map[string]bool{"cancelled": exists}})
		case "parse", "generate":
			jobsMu.Lock()
			if _, exists := jobs[request.ID]; exists {
				jobsMu.Unlock()
				fail(request.ID, "DUPLICATE_ID", "Request ID is already in flight")
				continue
			}
			ctx, cancel := context.WithCancel(context.Background())
			jobs[request.ID] = cancel
			jobsMu.Unlock()
			workers.Add(1)
			go func(r Request) {
				defer workers.Done()
				var result any
				if ctx.Err() == nil {
					if r.Method == "parse" {
						result = Parse(r.Source, r.File)
					} else {
						generated := Generate(r.Nodes, r.ScopeID, r.Components)
						for i := range generated.Diagnostics {
							generated.Diagnostics[i].File = r.File
						}
						result = generated
					}
				}
				jobsMu.Lock()
				delete(jobs, r.ID)
				cancelled := ctx.Err() != nil
				cancel()
				jobsMu.Unlock()
				if cancelled {
					fail(r.ID, "CANCELLED", "Request cancelled")
				} else {
					send(Response{Version: ProtocolVersion, ID: r.ID, Result: result})
				}
			}(request)
		default:
			fail(request.ID, "UNKNOWN_METHOD", "Unknown method: "+request.Method)
		}
	}
	// EOF drains successful work rather than cancelling it, which also makes
	// piped one-shot requests behave exactly like the persistent transport.
	workers.Wait()
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading compiler protocol: %w", err)
	}
	return writeErr
}
