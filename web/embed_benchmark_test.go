package web

import (
	"net/http/httptest"
	"testing"
)

func BenchmarkSPA(b *testing.B) {
	h := Handler(nil)
	for _, method := range []string{"GET", "HEAD"} {
		b.Run(method, func(b *testing.B) {
			req := httptest.NewRequest(method, "/agents/example", nil)
			b.ReportAllocs()
			for b.Loop() {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
		})
	}
}
