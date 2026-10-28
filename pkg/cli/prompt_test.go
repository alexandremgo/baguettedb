package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestPrompterConfirm checks that confirm reads an empty answer, the end of the input, y and yes as a yes, n and no as
// a no, in any case, and asks again after any other answer.
func TestPrompterConfirm(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"\n", true},
		{"", true},
		{"Y\n", true},
		{"yes\n", true},
		{"n\n", false},
		{" No \n", false},
		{"maybe\nn\n", false},
	} {
		p := newPrompter(context.Background(), strings.NewReader(tc.input), io.Discard)
		got, err := p.confirm("End every web session?")
		if err != nil || got != tc.want {
			t.Errorf("confirm with %q = %t, %v; want %t", tc.input, got, err, tc.want)
		}
	}
}

// TestPrompter_CancelEndsARead checks that a question waiting for an answer that never comes ends as soon as the
// context does, with the context's error, as Ctrl-C cancels it.
func TestPrompter_CancelEndsARead(t *testing.T) {
	stdin, typist := io.Pipe()
	t.Cleanup(func() {
		if err := typist.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	p := newPrompter(ctx, stdin, io.Discard)

	asked := make(chan error, 1)
	go func() {
		_, err := p.ask("Email", "ada@example.com")
		asked <- err
	}()
	cancel()
	select {
	case err := <-asked:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("ask after the cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask still waits for its answer after the cancel")
	}
}
