package fakebedrock

import "testing"

func TestNewRejectsUnknownMode(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unknown mode was accepted")
		}
	}()
	s := New("throtle")
	defer s.Close()
}
