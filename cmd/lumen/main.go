// Command lumen is a Lightroom-style RAW editor organized as albums, with
// an MCP server so Claude can edit photos while you watch.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"

	"lumen/internal/app"
	"lumen/internal/develop"
	"lumen/internal/mcp"
	"lumen/internal/raw"
	"lumen/internal/server"
)

const defaultPort = 5866

func usage() {
	fmt.Fprint(os.Stderr, `Lumen — RAW photo editor with masonry albums, with MCP for Claude.

Usage:
  lumen [open]          start Lumen if needed and open its window
  lumen start           start the app server in the background if needed (flag: -port)
  lumen serve           run the app server (flags: -port, -no-open)
  lumen mcp             MCP stdio bridge for Claude Code (starts the app on first use)
  lumen install         register the MCP server with Claude Code (user scope)
  lumen render FILE OUT render a file with default settings (debug)
  lumen info FILE       dump TIFF/RAW structure (debug)
`)
}

func dataDir() string {
	if d := os.Getenv("LUMEN_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Pictures", "Lumen")
}

func base(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

// appPort is the port of the app server (LUMEN_PORT overrides it).
func appPort() int {
	if p, err := strconv.Atoi(os.Getenv("LUMEN_PORT")); err == nil && p > 0 {
		return p
	}
	return defaultPort
}

func main() {
	cmd := "open"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	args := []string{}
	if len(os.Args) > 2 {
		args = os.Args[2:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "open":
		openCmd()
	case "start":
		startCmd(args)
	case "mcp":
		bridge()
	case "install":
		install()
	case "render":
		render(args)
	case "info":
		if len(args) < 1 {
			usage()
			os.Exit(2)
		}
		if err := raw.DumpTIFF(os.Stdout, args[0]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", appPort(), "port on 127.0.0.1")
	noOpen := fs.Bool("no-open", false, "do not open the window")
	fs.Parse(args)
	// Keep the heap small: previews live on disk, memory only caches them.
	// The soft limit makes the collector work harder before growing.
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(700 << 20)
	// Take the port before opening the album: when two starts race (the
	// window and Claude's MCP bridge), the second must exit without
	// touching the album.
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}
	a, err := app.New(dataDir())
	if err != nil {
		log.Fatal(err)
	}
	if dir := os.Getenv("LUMEN_TRASH_DIR"); dir != "" {
		// Tests: "Move to Trash" moves files into this folder instead.
		a.Trash = func(paths []string) error {
			for _, p := range paths {
				if err := os.Rename(p, filepath.Join(dir, filepath.Base(p))); err != nil {
					return err
				}
			}
			return nil
		}
	}
	s := server.New(a, addr)
	// Instances with their own data folder (tests, scripts) never open
	// windows by themselves.
	s.AutoOpen = os.Getenv("LUMEN_HOME") == ""
	log.Printf("Lumen %s on %s (data in %s)", mcp.Version, s.URL(), dataDir())
	if !*noOpen {
		go func() {
			time.Sleep(300 * time.Millisecond)
			s.OpenUI(true)
		}()
	}
	go func() {
		// Flush the album on a timer as a safety net.
		for range time.Tick(30 * time.Second) {
			_ = a.Store.Flush()
		}
	}()
	if err := s.Serve(ln); err != nil {
		log.Fatal(err)
	}
}

func alive(port int) bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(base(port) + "/api/ping")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// startBackground launches "lumen serve -no-open" detached from the caller.
func startBackground(port int) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dataDir(), "lumen.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	c := exec.Command(exe, "serve", "-no-open", "-port", fmt.Sprint(port))
	c.Stdout, c.Stderr = logf, logf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

func openCmd() {
	port := appPort()
	if !alive(port) {
		if err := startBackground(port); err != nil {
			log.Fatal(err)
		}
		for i := 0; i < 100 && !alive(port); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	} else if server.NativeApp() == "" && clients(port) > 0 {
		// A browser window is already open: bring Chrome forward instead
		// of adding one. (Lumen.app is single-window: open just activates it.)
		_ = exec.Command("open", "-a", "Google Chrome").Run()
		return
	}
	server.OpenWindow(base(port) + "/")
}

// startCmd starts the app server in the background unless it is running,
// and waits until it answers. Lumen.app runs it on launch.
func startCmd(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	port := fs.Int("port", appPort(), "port")
	_ = fs.Parse(args)
	if !alive(*port) {
		if err := startBackground(*port); err != nil {
			log.Fatal(err)
		}
		for i := 0; i < 150 && !alive(*port); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !alive(*port) {
		fmt.Fprintf(os.Stderr, "lumen: the app did not start; see %s\n", filepath.Join(dataDir(), "lumen.log"))
		os.Exit(1)
	}
}

func clients(port int) int {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(base(port) + "/api/ping")
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	var p struct {
		Clients int `json:"clients"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&p)
	return p.Clients
}

func bridge() {
	port := appPort()
	b := &mcp.Bridge{
		Base:  base(port),
		Start: func() error { return startBackground(port) },
	}
	if err := b.Run(os.Stdin, os.Stdout); err != nil {
		mcp.Stderr("lumen mcp: %v", err)
		os.Exit(1)
	}
}

func install() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	_ = exec.Command("claude", "mcp", "remove", "-s", "user", "lumen").Run()
	c := exec.Command("claude", "mcp", "add", "-s", "user", "lumen", "--", exe, "mcp")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		log.Fatalf("claude mcp add failed: %v", err)
	}
	fmt.Println("Registered MCP server \"lumen\" for Claude Code (restart Claude Code to load it).")
}

func render(args []string) {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	size := fs.Int("size", 1600, "long edge (0 = full resolution)")
	fs.Parse(args)
	if fs.NArg() < 2 {
		usage()
		os.Exit(2)
	}
	t0 := time.Now()
	src, err := develop.LoadSource(fs.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	t, tn := src.Color.TempTint(src.Color.AsShot)
	fmt.Printf("decoded %s %dx%d in %v, as shot %.0fK %+.0f\n", src.Kind, src.W, src.H, time.Since(t0).Round(time.Millisecond), t, tn)
	s := develop.Defaults(src.Raw)
	opt := develop.RenderOptions{MaxW: *size, MaxH: *size, AllowFull: true}
	if *size == 0 {
		opt = develop.RenderOptions{Scale: 1, AllowFull: true}
	}
	t1 := time.Now()
	res, err := develop.Render(src, &s, opt)
	if err != nil {
		log.Fatal(err)
	}
	b, err := res.JPEG(90)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(fs.Arg(1), b, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rendered %dx%d in %v\n", res.W, res.H, time.Since(t1).Round(time.Millisecond))
}
