package librarian

// The librarian eval: golden questions against a small fixture world, asked
// of a REAL model. Opt-in (LIBRARIAN_EVAL=1) because it spends tokens and a
// model's answers vary; run it after changing the prompt, a tool, or the
// provider, e.g.:
//
//	LIBRARIAN_EVAL=1 go test ./internal/adapter/librarian -run TestEval -v
//
// The provider resolves like the binary's (nib llmconfig: llm.json,
// LLM_API_KEY/LLM_BASE_URL/LLM_MODEL, or the nib key store). Every case also
// checks groundedness:
// each mark:// URL the answer cites must exist in the fixture.

import (
	"context"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/nib/ai/llm"
	"github.com/latebit-io/nib/ai/llmconfig"
	niboauth "github.com/latebit-io/nib/ai/oauth"
)

// evalDoc is one fixture document.
type evalDoc struct {
	title, tags, body string
	editions          string // VERSIONS body; "" ⇒ one edition
}

// evalCorpus is the fixture world "ops": enough structure that the right
// answer needs the right tool, and nothing a model could know beforehand.
var evalCorpus = map[string]evalDoc{
	"/index.md": {title: "Ops hub", tags: "hub", body: "# Ops hub\n\nStart with [the deploy runbook](runbooks/deploy.md) and [the decisions](adr/).\n"},
	"/runbooks/deploy.md": {
		title: "Deploy runbook", tags: "runbook, deploy",
		body:     "# Deploy runbook\n\nShip with `quillctl ship --ring canary`, then widen.\n\n## Rollback\n\nRun `quillctl rewind --to last-green`. Never hand-edit the ring file.\n",
		editions: "# Version History: /runbooks/deploy.md\n\n- [v2](/runbooks/deploy.md/v2) - 2026-09-01T10:00:00Z\n- [v1](/runbooks/deploy.md/v1) - 2026-06-01T10:00:00Z\n",
	},
	"/runbooks/deploy.md/v1": {title: "Deploy runbook", body: "# Deploy runbook\n\nShip with `quillctl ship --all`.\n"},
	"/adr/0003-ledger-store.md": {
		title: "ADR 0003: the ledger lives in Postgres", tags: "adr, decision, database",
		body: "# ADR 0003: the ledger lives in Postgres\n\nWe chose Postgres over DynamoDB for the ledger: transactions across accounts.\n",
	},
}

// evalPorts serves evalCorpus through every port the librarian reads,
// recording which ones a run used.
type evalPorts struct {
	*fakePorts // the unused Reader/Graph/Map methods

	mu    sync.Mutex
	calls []string
}

func (p *evalPorts) record(call string) {
	p.mu.Lock()
	p.calls = append(p.calls, call)
	p.mu.Unlock()
}

func (p *evalPorts) used(call string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.calls, call)
}

func (p *evalPorts) Floor(context.Context) (domain.Floor, error) {
	p.record("worlds")
	return domain.Floor{Worlds: []domain.FloorWorld{{
		World: domain.WorldInfo{Name: "ops"},
		Docs:  []domain.FloorDoc{{Path: "/index.md", Title: "Ops hub"}},
	}}}, nil
}

func (p *evalPorts) NameIndex(context.Context, string, string) ([]domain.IndexEntry, error) {
	p.record("find")
	out := make([]domain.IndexEntry, 0, len(evalCorpus))
	for path, doc := range evalCorpus {
		out = append(out, domain.IndexEntry{World: "ops", Path: path, Title: doc.title})
	}
	return out, nil
}

func (p *evalPorts) Lookup(_ context.Context, q domain.CatalogQuery) (domain.CatalogResult, error) {
	p.record("lookup")
	result := domain.CatalogResult{BodyMatched: q.Match == domain.MatchBody}
	needle := strings.ToLower(q.Query)
	for path, doc := range evalCorpus {
		hay := strings.ToLower(doc.title + " " + doc.tags)
		if q.Match == domain.MatchBody {
			hay = strings.ToLower(doc.body)
		}
		if doc.tags == "" || !strings.Contains(hay, needle) || (q.Tag != "" && !strings.Contains(doc.tags, q.Tag)) {
			continue
		}
		result.Hits = append(result.Hits, domain.CatalogHit{Ref: domain.Ref{World: "ops", Path: path}, Title: doc.title})
	}
	return result, nil
}

func (p *evalPorts) Raw(_ context.Context, _, path string) (domain.RawDocument, error) {
	p.record("open")
	doc, ok := evalCorpus[path]
	if !ok {
		return domain.RawDocument{}, domain.ErrNotFound
	}
	return domain.RawDocument{Path: path, Body: doc.body, Metadata: map[string]string{"title": doc.title, "tags": doc.tags}}, nil
}

func (p *evalPorts) Versions(_ context.Context, _, path string) (domain.RawDocument, error) {
	p.record("versions")
	doc, ok := evalCorpus[path]
	if !ok {
		return domain.RawDocument{}, domain.ErrNotFound
	}
	return domain.RawDocument{Path: path, Body: doc.editions}, nil
}

func (p *evalPorts) ReadCached(context.Context, string, string) (domain.Document, error) {
	p.record("links")
	return domain.Document{}, domain.ErrNotFound
}

var citation = regexp.MustCompile(`mark://ops(/[^\s)\]#]*)`)

// evalProvider resolves the model the binary would use, nib key store
// included, or skips when the eval was not asked for.
func evalProvider(t *testing.T) llm.Provider {
	t.Helper()
	if os.Getenv("LIBRARIAN_EVAL") == "" {
		t.Skip("set LIBRARIAN_EVAL=1 to ask a real model the golden questions")
	}
	_, resolved := llmconfig.Resolve("")
	if !resolved.HasProvider() {
		if path, err := niboauth.DefaultKeyStorePath(); err == nil {
			if ks, err := niboauth.NewKeyStore(path); err == nil {
				llmconfig.WireStoredKey(resolved, ks)
			}
		}
	}
	if !resolved.HasProvider() {
		t.Fatal("LIBRARIAN_EVAL set but no LLM provider resolves (llm.json, LLM_API_KEY, or the nib key store)")
	}
	return resolved.NewProvider()
}

// TestEvalCappedRunStillAnswers holds the turn cap's promise against a real
// provider: a run that exhausts its tools still ends in an answer.
func TestEvalCappedRunStillAnswers(t *testing.T) {
	provider := evalProvider(t)
	ports := &evalPorts{fakePorts: newFakePorts()}
	l, err := New(Config{Provider: provider, Reader: ports, Catalog: ports, Graph: ports, Map: ports, DefaultWorld: "ops", MaxTurns: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ch, err := l.Ask(ctx, domain.LibrarianAsk{Conversation: "eval", Question: "How do I roll back a bad release?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for range ch {
	}
	hist := l.History("eval")
	if len(hist) != 1 || hist[0].Answer == "" {
		t.Fatalf("capped run left no answer: %+v", hist)
	}
	t.Logf("steps: %+v\nanswer:\n%s", hist[0].Steps, hist[0].Answer)
}

func TestEval(t *testing.T) {
	provider := evalProvider(t)

	cases := []struct {
		question  string
		mustCite  []string // paths the answer has to cite
		mustUse   []string // ports the librarian has to have read
		mustNever []string // text an honest answer never contains
	}{
		{question: "Where is the deploy runbook?", mustCite: []string{"/runbooks/deploy.md"}},
		{question: "What did we decide about the ledger's database, and why?", mustCite: []string{"/adr/0003-ledger-store.md"}, mustUse: []string{"open"}},
		{question: "How do I roll back a bad release?", mustCite: []string{"/runbooks/deploy.md"}, mustUse: []string{"open"}},
		{question: "What changed in the deploy runbook since its first edition?", mustCite: []string{"/runbooks/deploy.md"}, mustUse: []string{"versions"}},
		{question: "Which document covers our Kafka retention policy?", mustNever: []string{"kafka.md", "retention.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.question, func(t *testing.T) {
			ports := &evalPorts{fakePorts: newFakePorts()}
			l, err := New(Config{
				Provider: provider,
				Reader:   ports, Catalog: ports, Graph: ports, Map: ports,
				DefaultWorld: "ops",
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			ch, err := l.Ask(ctx, domain.LibrarianAsk{Conversation: "eval", Question: tc.question})
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}
			for range ch { // drain; the exchange is read from History
			}
			hist := l.History("eval")
			if len(hist) != 1 || hist[0].Answer == "" {
				t.Fatalf("no answer: %+v", hist)
			}
			answer := hist[0].Answer
			t.Logf("answer:\n%s", answer)

			cited := map[string]bool{}
			for _, m := range citation.FindAllStringSubmatch(answer, -1) {
				cited[m[1]] = true
				if _, ok := evalCorpus[m[1]]; !ok {
					t.Errorf("cites a document that does not exist: mark://ops%s", m[1])
				}
			}
			for _, path := range tc.mustCite {
				if !cited[path] {
					t.Errorf("does not cite mark://ops%s", path)
				}
			}
			for _, port := range tc.mustUse {
				if !ports.used(port) {
					t.Errorf("never used %s (used %v)", port, ports.calls)
				}
			}
			for _, bad := range tc.mustNever {
				if strings.Contains(strings.ToLower(answer), bad) {
					t.Errorf("answer invents %q", bad)
				}
			}
		})
	}
}
