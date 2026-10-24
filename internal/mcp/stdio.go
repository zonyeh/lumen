package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// Bridge speaks MCP over stdio for Claude Code and forwards tool calls to
// the running Lumen app, starting it on the first call.
type Bridge struct {
	Base   string       // http://127.0.0.1:5866
	Start  func() error // launches the app in the background
	client *http.Client
	once   sync.Once
}

func (b *Bridge) alive() bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(b.Base + "/api/ping")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func (b *Bridge) ensure() error {
	if b.alive() {
		return nil
	}
	if err := b.Start(); err != nil {
		return fmt.Errorf("could not start Lumen: %w", err)
	}
	for i := 0; i < 100; i++ {
		time.Sleep(150 * time.Millisecond)
		if b.alive() {
			return nil
		}
	}
	return fmt.Errorf("Lumen did not start (see ~/Pictures/Lumen/lumen.log)")
}

// Run serves stdin/stdout until EOF.
func (b *Bridge) Run(in io.Reader, out io.Writer) error {
	b.client = &http.Client{Timeout: 15 * time.Minute}
	local := &Server{}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var wmu sync.Mutex
	write := func(p []byte) {
		wmu.Lock()
		defer wmu.Unlock()
		out.Write(p)
		out.Write([]byte("\n"))
	}
	var wg sync.WaitGroup
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		msg := append([]byte(nil), line...)
		var probe struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(msg, &probe)
		if probe.Method != "tools/call" {
			if resp := local.Handle(msg); resp != nil {
				write(resp)
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			write(b.forward(msg, probe.ID))
		}()
	}
	wg.Wait()
	return sc.Err()
}

func (b *Bridge) forward(msg []byte, id json.RawMessage) []byte {
	fail := func(err error) []byte {
		return mustJSON(rpcResponse{JSONRPC: "2.0", ID: id, Result: &ToolResult{
			Content: []Content{{Type: "text", Text: err.Error()}}, IsError: true}})
	}
	if err := b.ensure(); err != nil {
		return fail(err)
	}
	req, _ := http.NewRequest("POST", b.Base+"/mcp", bytes.NewReader(msg))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fail(err)
	}
	if resp.StatusCode != 200 {
		return fail(fmt.Errorf("Lumen returned %s: %s", resp.Status, bytes.TrimSpace(body)))
	}
	return body
}

// Stderr logs bridge diagnostics without touching the protocol stream.
func Stderr(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
