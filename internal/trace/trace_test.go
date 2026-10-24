package trace

import (
	"errors"
	"testing"
)

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteFailure(t *testing.T) {
	want := errors.New("disk full")
	recorder := New(failingWriter{want})
	_, finish := recorder.Begin("test/model", "hello", nil)
	if err := finish(nil); !errors.Is(err, want) {
		t.Fatalf("trace write failure lost: %v", err)
	}
}
