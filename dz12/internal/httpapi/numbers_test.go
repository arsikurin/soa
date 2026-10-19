package httpapi

import (
	"strings"
	"testing"
)

func TestNumberPrepassBoundsWork(t *testing.T) {
	for _, body := range []string{
		strings.Repeat("[", 200) + "0" + strings.Repeat("]", 200),
		`{"` + strings.Repeat("x", 257) + `":1}`,
		`{"price":1e2147483647}`,
		`{"price":1e-2147483648}`,
		`{"value":` + strings.Repeat("1", 65) + `}`,
	} {
		if err := checkNumberEncoding([]byte(body)); err == nil {
			t.Errorf("accepted unbounded representation, length=%d", len(body))
		}
	}
	for _, body := range []string{`{"price":1e2,"text":"1e100000"}`, `{"items":[{"quantity":999}]}`, `{"price":0.01}`, `{"number":1e-18}`} {
		if err := checkNumberEncoding([]byte(body)); err != nil {
			t.Fatalf("valid representation %s: %v", body, err)
		}
	}
}
