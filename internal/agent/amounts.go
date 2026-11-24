// This file checks the money amounts and percentages in an answer against
// what the turn read: each amount, such as $95.18, and each percentage,
// such as 12.3%, should appear in a tool's result, an excerpt the prompt
// held, or the user's own words. One that appears in none gets a notice
// under the answer, so the user knows the model worked it out or recalled
// it rather than read it. See issue #241.

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

// percentPattern finds a percentage in an answer: 12%, 8.4 % or 12.3
// percent. Group 1 holds the number; a minus sign before it doesn't
// count, so "down 8.4%" and "-8.4%" both match a read 8.4.
var percentPattern = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s?(?:%|percent\b)`)

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

// readNumbers returns every number in read, in hundredths, as a set.
func readNumbers(read []string) map[int64]bool {
	seen := map[int64]bool{}
	for _, text := range read {
		for _, n := range numberPattern.FindAllString(text, -1) {
			if c, ok := cents(n); ok {
				seen[c] = true
			}
		}
	}
	return seen
}

// unreadAmounts returns the amounts in answer, as the answer writes them,
// whose value appears in nothing in read. It skips fenced and inline code,
// which may hold sample figures, and lists each amount once.
func unreadAmounts(answer string, read []string) []string {
	seen := readNumbers(read)
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

// unreadPercents returns the percentages in answer, as the answer writes
// them, whose number appears in nothing in read, under the same rules as
// unreadAmounts. 0% and 100% never count: they say "none" and "all".
//
// A real turn said the market's slide had taken one chip maker "down
// ~12%" in ten days. The turn had fetched no prices for it, and the fall
// was 30%; the amounts check let it pass, since it had no currency sign.
func unreadPercents(answer string, read []string) []string {
	seen := readNumbers(read)
	seen[0], seen[10000] = true, true
	var out []string
	for _, m := range percentPattern.FindAllStringSubmatch(inlineCode.ReplaceAllString(withoutCode(answer), ""), -1) {
		c, ok := cents(m[1])
		if !ok || seen[c] {
			continue
		}
		seen[c] = true // name each percentage once
		out = append(out, strings.TrimSpace(m[0]))
	}
	return out
}

// namedList writes items as "a, b, c" and, past maxAmountsNamed of them,
// " and 3 more".
func namedList(items []string) string {
	if len(items) <= maxAmountsNamed {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:maxAmountsNamed], ", ") + " and " + strconv.Itoa(len(items)-maxAmountsNamed) + " more"
}

// amountsNotice returns the notice for amounts that unreadAmounts found,
// or "" for none.
func amountsNotice(amounts []string) string {
	if len(amounts) == 0 {
		return ""
	}
	what := "This amount isn't"
	if len(amounts) > 1 {
		what = "These amounts aren't"
	}
	return what + " in what Meru read this turn, so the model worked it out: " +
		namedList(amounts) + ". Check before you rely on it."
}

// percentsNotice returns the notice for percentages that unreadPercents
// found, or "" for none. A percentage may come from the model's memory as
// well as its arithmetic, so the notice names both.
func percentsNotice(percents []string) string {
	if len(percents) == 0 {
		return ""
	}
	what := "This percentage isn't"
	if len(percents) > 1 {
		what = "These percentages aren't"
	}
	return what + " in what Meru read this turn, so the model worked it out or recalled it: " +
		namedList(percents) + ". Check before you rely on it."
}
