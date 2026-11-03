package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Victoroide/axiom-engine/internal/domain"
)

func FuzzStrictJSON(f *testing.F) {
	for _, body := range []string{
		`{}`, `null`, `[]`, ``, `{} {}`,
		`{"student_id":"s","failed_topics":["Recursion"],"vark_profile":"visual"}`,
		`{"failed_topics":[]}`, `{"failed_topics":null}`, `{"failed_topics":[null]}`,
		`{"student_id":"a","student_id":"b"}`, `{"Student_ID":"s"}`,
		`{"telemetry":{"session_id":"s","timestamp_unix":1,"duration_ms":1.5}}`,
		`{"telemetry":{"duration_ms":1e999}}`, `{"telemetry":{"timestamp_unix":9223372036854775808}}`,
		`{"` + strings.Repeat("\u202e", 1000) + `":0}`,
	} {
		f.Add([]byte(body))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		var req domain.AdaptivePlanRequest
		if err := decodeStrictJSON(body, &req); err != nil {
			if len(err.Error()) > 1024 {
				t.Fatalf("error exceeds 1024 bytes: %d", len(err.Error()))
			}
			return
		}
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal accepted request: %v", err)
		}
		var got domain.AdaptivePlanRequest
		if err := decodeStrictJSON(data, &got); err != nil {
			t.Fatalf("decode marshaled request: %v", err)
		}
		if !reflect.DeepEqual(req, got) {
			t.Fatalf("request changed after round trip: before=%+v after=%+v", req, got)
		}
	})
}
