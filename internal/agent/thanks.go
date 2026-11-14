// This file holds the route rule for a short message of thanks, praise or
// greeting, such as "great job Meru! Yay!!!" or "thanks!". See
// ARCHITECTURE.md, "Routing".

package agent

import (
	"regexp"
	"slices"
	"strings"
)

// thanksCore are the words that make a message thanks, praise or a
// greeting. A message needs one of them, or a typo of one (see typoOf).
var thanksCore = []string{
	"thanks", "thank", "thx", "ty", "great", "good", "nice", "awesome",
	"amazing", "perfect", "cool", "excellent", "wonderful", "brilliant",
	"fantastic", "sweet", "helpful", "yay", "wow", "love", "appreciate",
	"appreciated", "cheers", "hi", "hello", "hey", "morning", "afternoon",
	"evening", "bye", "goodbye",
}

// thanksFiller are the other words such a message may hold: "you did a
// great job", "that works", "thank you so much, Meru". A message holds
// only these and thanksCore, so a single word outside both, such as
// "read" in "thanks, now read the other one", leaves the message to the
// router. "yes", "ok" and "sure" aren't here: they often answer a question
// Meru asked, such as "Would you like me to read it?", and that turn needs
// the tools. Nor are "now", "please" or "can", which come before a request.
var thanksFiller = []string{
	"you", "your", "so", "much", "very", "really", "truly", "a", "an", "the",
	"lot", "job", "work", "well", "done", "did", "doing", "is", "are", "was",
	"it", "that", "this", "worked", "works", "such", "all", "again", "meru",
	"haha", "lol",
}

// maxThanksWords caps the message. Thanks run short; a longer message
// made only of these words is rare, and the router can take it.
const maxThanksWords = 10

// letters matches each run of letters in a message, so punctuation,
// digits and emoji split words and drop out. \p{L} is any letter.
var letters = regexp.MustCompile(`\p{L}+`)

// thanksOnly reports whether message is a short message of thanks, praise
// or greeting: no question mark, at most maxThanksWords words, each word
// in thanksCore or thanksFiller or a typo of a core word, and at least one
// core word or typo of one. Such a message needs nothing from the user's
// files or tools, so Respond sends it direct whatever the router said, and
// with [routing] answers = "routed" the fast model replies.
//
// Real turns showed why. The router scored "great job Meru! Yay!!!" at
// 0.391 for search+tools and 0.357 for direct, below min_confidence, so
// the turn took the fallback, search+tools. The search and every tool's
// schema made a 39,844-token prompt, and the main model took 52 seconds
// to read it before a 57-token thank-you. Once the rule took exact words
// only, "you did a greewat job meru" missed it on the typo and on "did",
// and took 47 seconds the same way.
func thanksOnly(message string) bool {
	if strings.Contains(message, "?") {
		return false
	}
	words := letters.FindAllString(strings.ToLower(message), -1)
	if len(words) == 0 || len(words) > maxThanksWords {
		return false
	}
	core := false
	for _, w := range words {
		switch {
		case slices.Contains(thanksCore, w) || typoOf(w, thanksCore):
			core = true
		case slices.Contains(thanksFiller, w):
		default:
			return false
		}
	}
	return core
}

// typoOf reports whether w is a likely typo of one of words: it starts
// with the same letter, and is one edit away for a word of four or five
// letters, or two edits for a longer one. A word under four letters is no
// typo; "hey" and "yay" must be spelled right. The first letter and the
// small budget keep real words out: "create" isn't "great", nor "grant".
func typoOf(w string, words []string) bool {
	if len(w) < 4 {
		return false
	}
	most := 1
	if len(w) >= 6 {
		most = 2
	}
	for _, c := range words {
		if len(c) >= 4 && w[0] == c[0] && editDistance(w, c) <= most {
			return true
		}
	}
	return false
}

// editDistance returns how many single-letter edits turn a into b: an
// insert, a delete, a change, or a swap of two letters side by side, so
// "thnaks" is one edit from "thanks". It works on bytes, which is right
// for the ASCII words it compares.
func editDistance(a, b string) int {
	// d[i][j] is the distance between the first i letters of a and the
	// first j of b. make builds each row of the table.
	d := make([][]int, len(a)+1)
	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(a)][len(b)]
}
