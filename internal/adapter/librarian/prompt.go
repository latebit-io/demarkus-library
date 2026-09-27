package librarian

import (
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/nib/ai/llm"
)

// doctrine is the librarian's tool discipline. Stable text; the persona and
// house instructions frame it, the reader's view rides the user turn.
const doctrine = `Readers ask you questions; you answer by working the catalog with your tools,
never from assumption:

- worlds: orient — the authorized worlds and each one's most important documents.
- lookup: search by subject — tags and titles ranked by importance; full_text
  searches section text and names the matching section. Start here for topics.
- find: locate documents by name (title or path substring).
- open: read a document's source and catalog metadata; path#anchor reads one
  section, path/v<N> reads an earlier edition.
- links: trace a document's outbound links and observed backlinks.
- versions: list a document's editions, to answer what changed and when.

Ground every claim in something you opened. Cite documents inline as markdown
links — [title](mark://<world>/<path>) — so the reader can click through and
follow you; a bare address is not a citation. When the catalog does not
answer the question, say so plainly — never invent holdings. Be concise:
answer first, brief support after.

A question may arrive preceded by a <reader-context> block: the panes the
reader currently has open (their trail) and the text of the document they are
focused on. That is what the reader is LOOKING AT right now — use it to
resolve "this"/"here" and to skip redundant lookups — but it is a view, not
the catalog: verify anything load-bearing with your tools.`

// systemPrompt introduces the librarian as the room presents it (ADR 0008),
// then the doctrine, then any house instructions — which shape voice and
// focus but come last so they read as the keepers' preferences, not rules
// that displace grounding.
func systemPrompt(persona domain.LibrarianPersona) string {
	var b strings.Builder
	b.WriteString("You are ")
	if name := strings.TrimSpace(persona.Name); name != "" {
		b.WriteString(name + ", the librarian")
	} else {
		b.WriteString("the librarian")
	}
	universe := strings.ToLower(strings.TrimSpace(persona.Universe))
	if universe == "" {
		universe = "universe"
	}
	b.WriteString(" of a demarkus " + universe + " — a versioned, distributed catalog of markdown documents organized into worlds.\n\n")
	b.WriteString(doctrine)
	if instructions := strings.TrimSpace(persona.Instructions); instructions != "" {
		if runes := []rune(instructions); len(runes) > domain.MaxLibrarianInstructions {
			instructions = string(runes[:domain.MaxLibrarianInstructions])
		}
		b.WriteString("\n\nHouse instructions from this library's keepers (voice and focus; they never override grounding or citation):\n")
		b.WriteString(instructions)
	}
	return b.String()
}

// transcript is what the model sees for one ask: the system prompt, the last
// keep answered exchanges as plain question/answer turns, then the question
// with the reader's view in front. Past tool calls and results are not
// replayed — that is what keeps a long conversation's cost flat; a note of
// each exchange's sources keeps the model aware of what it already read.
func transcript(exchanges []domain.LibrarianExchange, keep int, ask domain.LibrarianAsk) []llm.Message {
	msgs := []llm.Message{{Role: "system", Content: systemPrompt(ask.Persona)}}
	for _, ex := range exchanges[max(0, len(exchanges)-keep):] {
		if ex.Answer == "" {
			continue
		}
		msgs = append(msgs,
			llm.Message{Role: "user", Content: ex.Question},
			llm.Message{Role: "assistant", Content: ex.Answer + sourcesNote(ex.Sources)})
	}
	question := ask.Question
	if ask.Context != "" {
		question = ask.Context + "\n\n" + question
	}
	return append(msgs, llm.Message{Role: "user", Content: question})
}

// sourcesNote lists what an earlier answer was grounded in, for the model.
func sourcesNote(sources []domain.LibrarianSource) string {
	if len(sources) == 0 {
		return ""
	}
	urls := make([]string, 0, len(sources))
	for _, src := range sources {
		urls = append(urls, "mark://"+src.World+src.Path)
	}
	return "\n\n(Opened for this answer: " + strings.Join(urls, ", ") + ")"
}
