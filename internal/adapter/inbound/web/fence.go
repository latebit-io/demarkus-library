package web

import "strings"

// Markdown fence codec. demarkus serves only markdown, so a branding asset
// rides in a document's first fenced block (ADR 0008); this file is the only
// place that wrapping is written or read.

// fence is one fenced block: the info string's first word is the language,
// the rest is free text (a base64 block carries its content type there).
type fence struct {
	Lang    string
	Info    string
	Content string
}

// fencedDoc is an asset document: the H1 and summary keep the style gate
// quiet and make the document readable in any client.
type fencedDoc struct {
	Title   string
	Summary string
	Fence   fence
}

// markdown renders the document. The fence opens with more backticks than
// any run inside the payload, so content can never close it early.
func (d fencedDoc) markdown() string {
	content := strings.TrimRight(d.Fence.Content, "\n")
	ticks := strings.Repeat("`", max(3, longestBacktickRun(content)+1))
	var b strings.Builder
	b.WriteString("# " + d.Title + "\n\n" + d.Summary + "\n\n" + ticks + d.Fence.Lang)
	if d.Fence.Info != "" {
		b.WriteString(" " + d.Fence.Info)
	}
	b.WriteString("\n" + content + "\n" + ticks + "\n")
	return b.String()
}

// firstFence returns the document's first fenced block. An opener is three or
// more backticks and its closer must be at least as long (CommonMark), so a
// payload may contain shorter runs.
func firstFence(body string) (fence, bool) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		open := backtickPrefix(line)
		if open < 3 {
			continue
		}
		info := strings.TrimSpace(line[open:])
		if strings.Contains(info, "`") {
			continue // an inline code span, not a fence opener
		}
		lang, rest, _ := strings.Cut(info, " ")
		for j := i + 1; j < len(lines); j++ {
			if end := backtickPrefix(lines[j]); end >= open && strings.TrimSpace(lines[j][end:]) == "" {
				return fence{
					Lang:    strings.ToLower(lang),
					Info:    strings.TrimSpace(rest),
					Content: strings.Join(lines[i+1:j], "\n"),
				}, true
			}
		}
		return fence{}, false // unterminated
	}
	return fence{}, false
}

// backtickPrefix counts the leading backticks of a line.
func backtickPrefix(line string) int {
	return len(line) - len(strings.TrimLeft(line, "`"))
}

// longestBacktickRun is the longest sequence of consecutive backticks in s.
func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}
