package browser

import (
	"errors"
	"reflect"
	"testing"

	"github.com/chromedp/cdproto/runtime"
)

func TestWildcardRegexp(t *testing.T) {
	re, err := wildcardRegexp("https://example.test/*?done")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("https://example.test/a?done") {
		t.Fatal("pattern did not match")
	}
	if re.MatchString("http://example.test/a?done") {
		t.Fatal("pattern matched wrong scheme")
	}
}

func TestEventMatches(t *testing.T) {
	if !eventMatches("Runtime.consoleAPICalled", &runtime.EventConsoleAPICalled{}) {
		t.Fatal("runtime event did not match")
	}
	if eventMatches("Page.loadEventFired", &runtime.EventConsoleAPICalled{}) {
		t.Fatal("wrong event matched")
	}
}

func TestParameterValidation(t *testing.T) {
	if _, err := boolParam(map[string]any{"full-page": "yes"}, "full-page", false); err == nil {
		t.Fatal("accepted string boolean")
	}
	if got, err := numberParam(map[string]any{"x": 3}, "x", 0); err != nil || got != 3 {
		t.Fatalf("numberParam = %v, %v", got, err)
	}
	if err := validateURL("javascript:alert(1)"); err == nil {
		t.Fatal("accepted javascript URL")
	}
	var se *serviceError
	if !errors.As(validateURL("relative"), &se) || se.code != "invalid_params" {
		t.Fatalf("unexpected URL error: %v", validateURL("relative"))
	}
}

func TestResponseShape(t *testing.T) {
	r := errorResponse(fail("not_found", "missing"))
	if r.OK || r.Error == nil || r.Error.Code != "not_found" {
		t.Fatalf("response = %#v", r)
	}
	if !reflect.DeepEqual(keySequence("Enter"), keySequence("return")) {
		t.Fatal("key aliases differ")
	}
}
