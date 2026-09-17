package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/gorilla/websocket"
)

func TestSessionClosePropagatesCDPFailuresAndRetainsRetryIdentity(t *testing.T) {
	for _, failure := range []string{"query", "close", "unconfirmed"} {
		t.Run(failure, func(t *testing.T) {
			var mu sync.Mutex
			failing, foreignClosed := true, false
			targets := map[string]string{"owned": "", "popup": "owned", "foreign": ""}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				for {
					var command struct {
						ID     int64  `json:"id"`
						Method string `json:"method"`
						Params struct {
							TargetID string `json:"targetId"`
						} `json:"params"`
					}
					if err := conn.ReadJSON(&command); err != nil {
						return
					}
					response := map[string]any{"id": command.ID, "result": map[string]any{}}
					mu.Lock()
					switch command.Method {
					case "Target.getTargets":
						if failing && failure == "query" {
							delete(response, "result")
							response["error"] = map[string]any{"code": -32000, "message": "injected query failure"}
						} else {
							var infos []*target.Info
							for id, opener := range targets {
								infos = append(infos, &target.Info{TargetID: target.ID(id), OpenerID: target.ID(opener), Type: "page"})
							}
							response["result"] = map[string]any{"targetInfos": infos}
						}
					case "Target.closeTarget":
						id := command.Params.TargetID
						if id == "foreign" {
							foreignClosed = true
						}
						if failing && failure == "close" && id == "popup" {
							delete(response, "result")
							response["error"] = map[string]any{"code": -32000, "message": "injected close failure"}
						} else if !failing || failure != "unconfirmed" {
							delete(targets, id)
						}
					default:
						delete(response, "result")
						response["error"] = map[string]any{"code": -32601, "message": "unexpected method"}
					}
					mu.Unlock()
					if err := conn.WriteJSON(response); err != nil {
						return
					}
				}
			}))
			t.Cleanup(server.Close)
			ctx, cancel := chromedp.NewContext(context.Background())
			t.Cleanup(cancel)
			browser, err := chromedp.NewBrowser(ctx, "ws"+strings.TrimPrefix(server.URL, "http"))
			if err != nil {
				t.Fatal(err)
			}
			chromedp.FromContext(ctx).Browser = browser
			mgr := newManager(context.Background(), t.TempDir())
			t.Cleanup(mgr.close)
			root := t.TempDir()
			p := mgr.project(root, projectKey(root))
			s := &session{project: p, parentCtx: ctx, thread: "cleanup", dir: t.TempDir(), tabs: make(map[target.ID]*browserTab), ownedTargets: map[target.ID]struct{}{"owned": {}}}
			p.sessions[s.thread] = s
			request := Request{Project: root, Thread: s.thread, Action: "session-close"}
			if _, err := mgr.dispatch(context.Background(), request); err == nil {
				t.Fatal("session-close reported success while Chrome cleanup failed")
			} else if failure != "unconfirmed" && !strings.Contains(err.Error(), "injected "+failure+" failure") {
				t.Fatalf("CDP failure was not propagated: %v", err)
			}
			if p.sessions[s.thread] != s {
				t.Fatal("failed cleanup discarded its retry identity")
			}
			_, openErr := s.createTab(context.Background(), "about:blank", false)
			var commandErr *Error
			if !errors.As(openErr, &commandErr) || commandErr.Code != "not_found" {
				t.Fatalf("failed-cleanup session attempted to allocate another target: %v", openErr)
			}
			mu.Lock()
			failing = false
			if failure == "close" {
				// The failed popup closes itself, leaving a child for the retry.
				delete(targets, "popup")
				targets["descendant"] = "popup"
			}
			mu.Unlock()
			if _, err := mgr.dispatch(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if p.sessions[s.thread] != nil {
				t.Fatal("successful retry retained session")
			}
			mu.Lock()
			defer mu.Unlock()
			if foreignClosed || len(targets) != 1 {
				encoded, _ := json.Marshal(targets)
				t.Fatalf("cleanup lost ownership/isolation: foreignClosed=%v targets=%s", foreignClosed, encoded)
			}
		})
	}
}
