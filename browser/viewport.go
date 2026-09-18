package browser

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

const (
	defaultViewportWidth  = 1440
	defaultViewportHeight = 900
)

// A stable desktop viewport avoids responsive-layout changes as viewers connect.
func defaultViewport() *emulation.SetDeviceMetricsOverrideParams {
	return emulation.SetDeviceMetricsOverride(defaultViewportWidth, defaultViewportHeight, 1, false).
		WithScreenWidth(defaultViewportWidth).WithScreenHeight(defaultViewportHeight)
}

// Emulation alone does not enlarge headed Chrome's native screencast surface.
func headedWindowSize() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		c := chromedp.FromContext(ctx)
		browserCtx := cdp.WithExecutor(ctx, c.Browser)
		windowID, _, err := browser.GetWindowForTarget().WithTargetID(c.Target.TargetID).Do(browserCtx)
		if err != nil {
			return fmt.Errorf("get headed browser window: %w", err)
		}
		if err := browser.SetContentsSize(windowID).
			WithWidth(defaultViewportWidth).WithHeight(defaultViewportHeight).Do(browserCtx); err != nil {
			return fmt.Errorf("size headed browser contents: %w", err)
		}
		return nil
	})
}
