package main

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/controlplane"
	"github.com/gorilla/websocket"
)

func TestServeDefaults(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	cmd := newServeCommand()
	addr, _ := cmd.Flags().GetString("addr")
	dir, _ := cmd.Flags().GetString("data-dir")
	expected, err := defaultDataDir()
	if err != nil || addr != "0.0.0.0:8281" || dir != expected {
		t.Fatal(addr, dir, err)
	}
	if !cmd.Flags().Lookup("owned").Hidden {
		t.Fatal("ownership flag should be internal")
	}
	t.Setenv("TED_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if dir, err := defaultDataDir(); err != nil || dir != filepath.Join(home, ".ted", "controlplane") {
		t.Fatal(dir, err)
	}
	t.Setenv("TED_HOME", "~/custom")
	if dir, err := defaultDataDir(); err != nil || dir != filepath.Join(home, "custom", "controlplane") {
		t.Fatal(dir, err)
	}

}

func TestServeShutdownClosesWebSocketsAndReleasesStore(t *testing.T) {
	providers := []agent.Provider{agent.NewCodexProvider(nil)}
	dir := t.TempDir()
	service, err := controlplane.NewService(dir, nil, providers)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveControlPlane(ctx, listener, service) }()
	base := "http://" + listener.Addr().String()
	if err := checkServer(ctx, base); err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(base, "http")+"/v1/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked")
	}
	_ = ws.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("websocket survived owner shutdown")
	}
	c := &http.Client{Timeout: time.Second}
	if res, err := c.Get(base + "/health"); err == nil {
		res.Body.Close()
		t.Fatal("HTTP listener survived shutdown")
	}
	reopened, err := controlplane.NewService(dir, nil, providers)
	if err != nil {
		t.Fatal("store lock not released:", err)
	}
	_ = reopened.Close(context.Background())
}

func TestServeBindFailureBeforeCredentials(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	err = runServe(context.Background(), listener.Addr().String(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatal(err)
	}
}
