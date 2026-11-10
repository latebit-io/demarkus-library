package domain

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Name matching over the catalog's name index (ADR 0006 §3). Every query term
// must match an entry's title, tags or place; an entry ranks by its worst
// term, and ties keep the index's order, which is the catalog's own ranking.

// Match tiers, best first.
const (
	tierTitlePrefix = iota // the title starts with the term
	tierTitleWord          // a word of the title starts with it
	tierTag                // a tag, or the value of an axis tag, is the term
	tierTitle              // the title contains it
	tierPlace              // a tag starts with it, or the world or path contains it
	tierLoose              // its letters appear in order in the title
	tierNone
)

// minLoose is the shortest term matched loosely: a shorter one's letters
// appear in order in nearly every title.
const minLoose = 3

// candidate is an entry's matchable text, lowercased once per search.
type candidate struct {
	title, place string
	tags         []string
}

func newCandidate(e *IndexEntry) candidate {
	c := candidate{
		title: strings.ToLower(e.Title),
		place: strings.ToLower(e.World + e.Path),
		tags:  make([]string, len(e.Tags)),
	}
	for i, tag := range e.Tags {
		c.tags[i] = strings.ToLower(tag)
	}
	return c
}

// tier is how well one lowercase term matches, tierNone when it does not.
func (c *candidate) tier(term string) int {
	switch {
	case strings.HasPrefix(c.title, term):
		return tierTitlePrefix
	case startsWord(c.title, term):
		return tierTitleWord
	case slices.ContainsFunc(c.tags, func(tag string) bool { return tagIs(tag, term) }):
		return tierTag
	case strings.Contains(c.title, term):
		return tierTitle
	case strings.Contains(c.place, term) || slices.ContainsFunc(c.tags, func(tag string) bool { return strings.HasPrefix(tag, term) }):
		return tierPlace
	case len(term) >= minLoose && inOrder(c.title, term):
		return tierLoose
	}
	return tierNone
}

// tagIs reports a tag that is the term, or an axis tag (status:accepted)
// whose value is.
func tagIs(tag, term string) bool {
	if tag == term {
		return true
	}
	_, value, axis := strings.Cut(tag, ":")
	return axis && value == term
}

// startsWord reports term at the start of a word of s: after a rune that is
// neither letter nor digit.
func startsWord(s, term string) bool {
	for from := 0; from < len(s); {
		i := strings.Index(s[from:], term)
		if i < 0 {
			return false
		}
		at := from + i
		before, _ := utf8.DecodeLastRuneInString(s[:at])
		if at > 0 && !unicode.IsLetter(before) && !unicode.IsDigit(before) {
			return true
		}
		_, size := utf8.DecodeRuneInString(s[at:])
		from = at + max(size, 1)
	}
	return false
}

// inOrder reports term's bytes appearing in s in order, gaps allowed.
func inOrder(s, term string) bool {
	matched := 0
	for i := 0; i < len(s) && matched < len(term); i++ {
		if s[i] == term[matched] {
			matched++
		}
	}
	return matched == len(term)
}

// RankIndex returns the entries every term of q matches, best first, at most
// limit of them. The pointers are into entries.
func RankIndex(q string, entries []IndexEntry, limit int) []*IndexEntry {
	terms := strings.Fields(strings.ToLower(q))
	if len(terms) == 0 {
		return nil
	}
	type ranked struct {
		entry *IndexEntry
		worst int
	}
	var hits []ranked
	for i := range entries {
		c := newCandidate(&entries[i])
		worst := 0
		for _, term := range terms {
			if worst = max(worst, c.tier(term)); worst == tierNone {
				break
			}
		}
		if worst < tierNone {
			hits = append(hits, ranked{&entries[i], worst})
		}
	}
	slices.SortStableFunc(hits, func(a, b ranked) int { return cmp.Compare(a.worst, b.worst) })
	hits = hits[:min(len(hits), limit)]
	out := make([]*IndexEntry, len(hits))
	for i := range hits {
		out[i] = hits[i].entry
	}
	return out
}
