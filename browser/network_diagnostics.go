package browser

import (
	"fmt"

	"github.com/chromedp/cdproto/network"
)

// chromedp enables Network on attachment. Keep only failure metadata here;
// raw headers, cookies and bodies belong in explicitly requested CDP output.
func (t *browserTab) handleNetworkDiagnostic(event any) {
	switch e := event.(type) {
	case *network.EventLoadingFailed:
		item := map[string]any{
			"source": "network", "type": "loading_failed", "text": e.ErrorText,
			"request_id": e.RequestID, "resource_type": e.Type, "canceled": e.Canceled,
		}
		if e.BlockedReason != "" {
			item["blocked_reason"] = e.BlockedReason
		}
		if e.CorsErrorStatus != nil {
			item["cors_error"] = e.CorsErrorStatus.CorsError
		}
		t.appendError(item)
	case *network.EventResponseReceived:
		if e.Response == nil || e.Response.Status < 400 {
			return
		}
		t.appendError(map[string]any{
			"source": "network", "type": "http_error",
			"text":       fmt.Sprintf("HTTP %d", e.Response.Status),
			"request_id": e.RequestID, "resource_type": e.Type,
			"status": e.Response.Status,
		})
	}
}
