package browser

import "github.com/chromedp/cdproto/emulation"

const (
	defaultViewportWidth  = 1440
	defaultViewportHeight = 900
)

// A stable desktop viewport avoids responsive-layout changes as viewers connect.
func defaultViewport() *emulation.SetDeviceMetricsOverrideParams {
	return emulation.SetDeviceMetricsOverride(defaultViewportWidth, defaultViewportHeight, 1, false).
		WithScreenWidth(defaultViewportWidth).WithScreenHeight(defaultViewportHeight)
}
