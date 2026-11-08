// This file checks the money amounts in an answer against what the turn
// read: each amount, such as $95.18, should appear in a tool's result, an
// excerpt the prompt held, or the user's own words. One that appears in none
// gets a notice under the answer, so the user knows the model worked it out
// rather than read it. See issue #241.

package agent

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// maxReadChars caps how much of what a turn read the check keeps: enough
// for a long receipt and a page of mail, and bounded so a turn that reads a
// dozen long files can't grow the turn's memory without limit.
const maxReadChars = 2 << 20

// maxAmountsNamed is how many unread amounts the notice lists; past that it
// says how many more there are.
const maxAmountsNamed = 5

// amountPattern finds a money amount in an answer: a currency sign or code
// before the number ($95.18, € 27, EUR 1,042.25) or a code after it
// (27.00 EUR). Group 1 or group 2 holds the number.
var amountPattern = regexp.MustCompile(`(?i)(?:[$€£₹¥]\s?|\b(?:usd|eur|gbp|inr|cad|aud|jpy|chf)\s?)-?(\d[\d,]*(?:\.\d{1,2})?)|(\d[\d,]*(?:\.\d{1,2})?)\s?(?:usd|eur|gbp|inr|cad|aud|jpy|chf)\b`)

// inlineCode matches a span of inline code, such as `echo $9.99`, which
// the check skips as it skips fenced code.
var inlineCode = regexp.MustCompile("`[^`\n]*`")

// numberPattern finds every number in what the turn read, with or without
// a currency mark, so "Guest room 286.00" counts for "$286" in the answer.
var numberPattern = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?`)

// cents returns s, a number such as "1,042.25", in hundredths, and whether
// it parsed. Comparing hundredths makes "$572" and "572.00" the same amount.
func cents(s string) (int64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if err != nil || math.IsInf(v, 0) || v > 1e13 {
		return 0, false
	}
	return int64(math.Round(v * 100)), true
}

// noteRead adds text, something the turn showed the model, to what the
// amounts check compares against, up to maxReadChars in all.
func (t *turn) noteRead(texts ...string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range texts {
		if s == "" || t.readChars >= maxReadChars {
			continue
		}
		if rest := maxReadChars - t.readChars; len(s) > rest {
			s = s[:rest]
		}
		t.read = append(t.read, s)
		t.readChars += len(s)
	}
}

// unreadAmounts returns the amounts in answer, as the answer writes them,
// whose value appears in nothing in read. It skips fenced and inline code,
// which may hold sample figures, and lists each amount once.
func unreadAmounts(answer string, read []string) []string {
	seen := map[int64]bool{}
	for _, text := range read {
		for _, n := range numberPattern.FindAllString(text, -1) {
			if c, ok := cents(n); ok {
				seen[c] = true
			}
		}
	}
	var out []string
	for _, m := range amountPattern.FindAllStringSubmatch(inlineCode.ReplaceAllString(withoutCode(answer), ""), -1) {
		num := m[1]
		if num == "" {
			num = m[2]
		}
		c, ok := cents(num)
		if !ok || seen[c] {
			continue
		}
		seen[c] = true // name each amount once
		out = append(out, strings.TrimSpace(m[0]))
	}
	return out
}

// amountsNotice returns the notice for amounts that unreadAmounts found,
// or "" for none.
func amountsNotice(amounts []string) string {
	if len(amounts) == 0 {
		return ""
	}
	list := amounts
	more := ""
	if len(list) > maxAmountsNamed {
		more = " and " + strconv.Itoa(len(list)-maxAmountsNamed) + " more"
		list = list[:maxAmountsNamed]
	}
	what := "This amount isn't"
	if len(amounts) > 1 {
		what = "These amounts aren't"
	}
	return what + " in what Meru read this turn, so the model worked it out: " +
		strings.Join(list, ", ") + more + ". Check before you rely on it."
}
