package fakebedrock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewRejectsUnknownMode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unknown mode was accepted")
		}
	}()
	s := New("throtle")
	defer s.Close()
}

func TestRepliesContainOnlyItems(t *testing.T) {
	for _, mode := range []string{"ok", "empty"} {
		t.Run(mode, func(t *testing.T) {
			s := New(mode)
			defer s.Close()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/model/test/converse", strings.NewReader(`{}`))
			reply := httptest.NewRecorder()
			s.ServeHTTP(reply, req)
			var body struct {
				Output struct {
					Message struct{ Content []struct{ Text string } }
				}
			}
			if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Output.Message.Content) != 1 {
				t.Fatal("missing response text")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body.Output.Message.Content[0].Text), &fields); err != nil {
				t.Fatal(err)
			}
			if len(fields) != 1 || fields["items"] == nil {
				t.Fatalf("response fields=%v", fields)
			}
		})
	}
}
