package librarian

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/nib/ai/llm"
	"github.com/latebit-io/nib/ai/llmtest"
)

// drain reads an Ask stream to its end. For a synctest bubble, where the
// retry waits outlast collect's deadline and a wedged run panics on its own.
func drain(ch <-chan domain.LibrarianEvent) []domain.LibrarianEvent {
	var evs []domain.LibrarianEvent
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

// retryNotices finds the trace lines that narrate a retry wait.
func retryNotices(evs []domain.LibrarianEvent) []int {
	var at []int
	for i, ev := range evs {
		if ev.Kind == domain.LibrarianTrace && strings.Contains(ev.Text, "provider is busy") {
			at = append(at, i)
		}
	}
	return at
}

func TestAsk_RetriesThrottledTurn(t *testing.T) {
	t.Parallel()

	throttled := &llm.StatusError{Provider: "test", StatusCode: http.StatusTooManyRequests, Code: "rate_limit_error"}
	synctest.Test(t, func(t *testing.T) {
		scripted := llmtest.NewScripted(llmtest.Text("fine now"))
		l := newTestLibrarian(t, llmtest.FailingFirst(2, throttled, scripted), newFakePorts())

		evs := drain(mustAsk(t, l, "k", "q"))

		notices := retryNotices(evs)
		if len(notices) != 2 ||
			!strings.Contains(evs[notices[0]].Text, "waiting 2s before trying again (1 of 4)") ||
			!strings.Contains(evs[notices[1]].Text, "waiting 4s before trying again (2 of 4)") {
			t.Errorf("retry trace lines at %v; want the 2s and 4s waits narrated in %+v", notices, evs)
		}
		if last := evs[len(evs)-1]; last.Kind != domain.LibrarianDone {
			t.Errorf("stream ended with %v; want done", last.Kind)
		}
		if got := scripted.Calls(); got != 1 {
			t.Errorf("served calls = %d; want 1 after the two throttled ones", got)
		}
		// The waits are part of the recorded work.
		hist := l.History("k")
		if len(hist) != 1 || len(hist[0].Steps) != 2 || hist[0].Answer != "fine now" {
			t.Errorf("history = %+v; want the answer and the two retry notices", hist)
		}
	})
}

func TestAsk_PermanentFailureIsNotRetried(t *testing.T) {
	t.Parallel()

	refused := &llm.StatusError{Provider: "test", StatusCode: http.StatusBadRequest}
	l := newTestLibrarian(t, llmtest.Failing(refused), newFakePorts())

	evs := collect(t, mustAsk(t, l, "k", "q"))

	if !slices.Contains(kinds(evs), domain.LibrarianError) || len(retryNotices(evs)) != 0 {
		t.Errorf("events = %+v; want the error surfaced with no retry narrated", evs)
	}
}

func TestAsk_CapAnswerRetriesAStreamedOverload(t *testing.T) {
	t.Parallel()

	// The cap's closing turn opens with an overload the provider streamed
	// inside a 200: it is retried, and narrated ahead of the answer.
	overloaded := []llm.StreamEvent{{Done: true, Err: &llm.StreamError{Provider: "test", Code: "overloaded_error"}}}
	synctest.Test(t, func(t *testing.T) {
		scripted := llmtest.NewScripted(
			llmtest.ToolCalls(llmtest.Call("c1", "worlds", "{}")),
			overloaded,
			overloaded,
			llmtest.Text("the floor."),
		)
		ports := newFakePorts()
		l, err := New(Config{
			Provider: scripted, Reader: ports, Catalog: ports, Graph: ports, Map: ports,
			DefaultWorld: "root", MaxTurns: 1,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		evs := drain(mustAsk(t, l, "k", "q"))

		answer := -1
		for i, ev := range evs {
			switch ev.Kind {
			case domain.LibrarianAnswer:
				answer = i
			case domain.LibrarianError:
				t.Errorf("retried overload surfaced as an error: %+v", ev)
			}
		}
		notices := retryNotices(evs)
		if len(notices) != 2 || answer < 0 || notices[1] > answer || evs[answer].Text != "the floor." {
			t.Errorf("notices at %v, answer at %d; want both waits narrated before the answer: %+v", notices, answer, evs)
		}
		if last := evs[len(evs)-1]; last.Kind != domain.LibrarianDone {
			t.Errorf("stream ended with %v; want done", last.Kind)
		}
		if got := scripted.Calls(); got != 4 {
			t.Errorf("provider calls = %d; want 4 (capped turn, two overloads, the answer)", got)
		}
	})
}
