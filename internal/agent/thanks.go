// This file holds the route rule for a short message of thanks, praise or
// greeting, such as "great job Meru! Yay!!!" or "thanks!". See
// ARCHITECTURE.md, "Routing".

package agent

import (
	"regexp"
	"slices"
	"strings"
)

// thanksWords are the words a message of thanks, praise or greeting is
// made of. A message holds only these, so a single word outside the list,
// such as "read" in "thanks, now read the other one", leaves the message
// to the router. "yes", "ok" and "sure" aren't here: they often answer a
// question Meru asked, such as "Would you like me to read it?", and that
// turn needs the tools.
var thanksWords = []string{
	"thanks", "thank", "thx", "ty", "you", "so", "much", "very", "a", "lot",
	"great", "good", "nice", "awesome", "amazing", "perfect", "cool",
	"excellent", "wonderful", "brilliant", "fantastic", "sweet", "helpful",
	"job", "work", "well", "done", "yay", "wow", "love", "it", "that",
	"this", "worked", "works", "appreciate", "appreciated", "cheers",
	"hi", "hello", "hey", "morning", "afternoon", "evening", "bye",
	"goodbye", "meru", "again", "haha", "lol",
}

// maxThanksWords caps the message. Thanks run short; a longer message
// made only of these words is rare, and the router can take it.
const maxThanksWords = 10

// letters matches each run of letters in a message, so punctuation,
// digits and emoji split words and drop out. \p{L} is any letter.
var letters = regexp.MustCompile(`\p{L}+`)

// thanksOnly reports whether message is a short message of thanks, praise
// or greeting: no question mark, at most maxThanksWords words, and every
// word in thanksWords. Such a message needs nothing from the user's files
// or tools, so Respond sends it direct whatever the router said, and with
// [routing] answers = "routed" the fast model replies.
//
// A real turn showed why. The router scored "great job Meru! Yay!!!" at
// 0.391 for search+tools and 0.357 for direct, below min_confidence, so
// the turn took the fallback, search+tools. The search and every tool's
// schema made a 39,844-token prompt, and the main model took 52 seconds
// to read it before a 57-token thank-you.
func thanksOnly(message string) bool {
	if strings.Contains(message, "?") {
		return false
	}
	words := letters.FindAllString(strings.ToLower(message), -1)
	if len(words) == 0 || len(words) > maxThanksWords {
		return false
	}
	for _, w := range words {
		if !slices.Contains(thanksWords, w) {
			return false
		}
	}
	return true
}
