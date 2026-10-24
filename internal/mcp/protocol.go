// Package mcp exposes Lumen to Claude over the Model Context Protocol
// (JSON-RPC 2.0), both as a streamable-HTTP endpoint inside the app and as
// a stdio bridge that starts the app on demand.
package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"lumen/internal/app"
)

// Version is reported to clients.
const Version = "0.1.0"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Content is one MCP content block.
type Content struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// ToolResult is the result of tools/call.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// Server answers MCP requests. With a nil App it can still describe itself
// (initialize, tools/list) but not run tools.
type Server struct {
	App *app.App
	// Call, when set, runs tools elsewhere (the stdio bridge forwards to the
	// running app).
	Call func(name string, args map[string]any) (*ToolResult, error)
}

// Handle processes one JSON-RPC message or batch and returns the response
// bytes (nil for notifications).
func (s *Server) Handle(body []byte) []byte {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var reqs []rpcRequest
		if err := json.Unmarshal(body, &reqs); err != nil {
			return mustJSON(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		}
		var out []rpcResponse
		for _, r := range reqs {
			if resp := s.handleOne(r); resp != nil {
				out = append(out, *resp)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return mustJSON(out)
	}
	var r rpcRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return mustJSON(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
	}
	resp := s.handleOne(r)
	if resp == nil {
		return nil
	}
	return mustJSON(resp)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (s *Server) handleOne(r rpcRequest) *rpcResponse {
	if len(r.ID) == 0 {
		return nil // notification
	}
	resp := &rpcResponse{JSONRPC: "2.0", ID: r.ID}
	switch r.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(r.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		resp.Result = map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "lumen", "title": "Lumen photo editor", "version": Version},
			"instructions":    Instructions,
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		list := make([]map[string]any, 0, len(Tools))
		for _, t := range Tools {
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		resp.Result = map[string]any{"tools": list}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(r.Params, &p); err != nil {
			resp.Error = &rpcError{-32602, "invalid params"}
			return resp
		}
		if p.Arguments == nil {
			p.Arguments = map[string]any{}
		}
		var res *ToolResult
		var err error
		if s.Call != nil {
			res, err = s.Call(p.Name, p.Arguments)
		} else {
			res, err = s.CallTool(p.Name, p.Arguments)
		}
		if err != nil {
			res = &ToolResult{Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}
		}
		resp.Result = res
	case "resources/list":
		resp.Result = map[string]any{"resources": []any{}}
	case "prompts/list":
		resp.Result = map[string]any{"prompts": []any{}}
	default:
		resp.Error = &rpcError{-32601, "method not found: " + r.Method}
	}
	return resp
}

// CallTool runs a tool against the app.
func (s *Server) CallTool(name string, args map[string]any) (res *ToolResult, err error) {
	t := toolIndex[name]
	if t == nil {
		return nil, fmt.Errorf("unknown tool %q", name)
	}
	if s.App == nil {
		return nil, fmt.Errorf("Lumen is not running")
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error in %s: %v", name, r)
		}
	}()
	return t.Run(&call{app: s.App, args: args, tool: name})
}
