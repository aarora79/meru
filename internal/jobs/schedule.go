// This file holds Schedule: parsing a five-field cron expression with the
// every-N-weeks rule, finding the next time it matches, and saying it in
// words for the clients.

package jobs

import (
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

// field describes one of the five cron fields: its name for error
// messages, the smallest and largest values, and the names it accepts in
// place of numbers.
type field struct {
	name     string
	min, max int
	names    []string // names[i] stands for min+i; nil for none
}

// The five fields, in the order a cron expression gives them.
var fields = [5]field{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12,
		names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	// 7 is Sunday too, as most crons allow; Parse folds it into 0.
	{name: "day of week", min: 0, max: 7,
		names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}},
}

// searchYears bounds Next: a schedule with no match within this many years,
// such as "0 0 31 2 *" (31 February), never runs, and Parse refuses it.
const searchYears = 5

// Schedule is a parsed schedule. Each cron field is a set of the values it
// allows, kept as a bitmask: bit n of hour is set when hour n matches, so
// "7" sets bit 7 and "1-5" sets bits 1 to 5. A uint64 has room for the 60
// minutes. Testing a value is one shift and one AND.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	// domStar and dowStar say the day fields began with "*". Cron's rule:
	// when both day fields are restricted, a day matches if either one
	// does; when one is "*", only the other decides.
	domStar, dowStar bool
	// everyWeeks is 1 for every week, 2 for every other week, and so on,
	// counted in whole weeks from from, the Monday of fromDay's week.
	// fromDay is the first day the job may run.
	everyWeeks int
	from       time.Time
	fromDay    time.Time // the date the user gave: the first day it may run
	expr       string    // the cron expression as given, for Describe
	text       [5]string
}

// Parse reads a cron expression with five fields separated by spaces:
// minute, hour, day of month, month and day of week. Each field is "*", a
// number, a name ("mon", "jan"), a range ("1-5"), a step ("*/15", "0-30/10")
// or a comma list of those. everyWeeks above 1 runs only every that many
// weeks, counted from the week of from, a date such as "2026-10-27" read in
// loc; from may be empty when everyWeeks is 0 or 1.
//
// It fails with the field and the reason, such as "hour: 25 is past 23",
// and for a schedule that never matches within searchYears years.
func Parse(expr string, everyWeeks int, from string, loc *time.Location) (Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("%q has %d fields; a schedule has 5: minute hour day-of-month month day-of-week", expr, len(parts))
	}
	s := Schedule{expr: strings.Join(parts, " "), everyWeeks: 1}
	// sets points at each of the five masks, so one loop fills them all.
	sets := [5]*uint64{&s.minute, &s.hour, &s.dom, &s.month, &s.dow}
	for i, p := range parts {
		mask, err := parseField(strings.ToLower(p), fields[i])
		if err != nil {
			return Schedule{}, fmt.Errorf("%s: %w", fields[i].name, err)
		}
		*sets[i] = mask
		s.text[i] = p
	}
	// Sunday may be written 7; fold it into 0.
	if s.dow&(1<<7) != 0 {
		s.dow = s.dow&^(1<<7) | 1
	}
	s.domStar = strings.HasPrefix(parts[2], "*")
	s.dowStar = strings.HasPrefix(parts[4], "*")

	switch {
	case everyWeeks < 0 || everyWeeks > 52:
		return Schedule{}, fmt.Errorf("every_weeks: %d isn't between 1 and 52", everyWeeks)
	case everyWeeks > 1:
		if from == "" {
			return Schedule{}, errors.New("every_weeks needs from, the date to count the weeks from, such as \"2026-10-27\"")
		}
		day, err := time.ParseInLocation(time.DateOnly, from, loc)
		if err != nil {
			return Schedule{}, fmt.Errorf("from: %q isn't a date like 2026-10-27", from)
		}
		s.everyWeeks, s.from, s.fromDay = everyWeeks, monday(day), day
	case from != "":
		return Schedule{}, errors.New("from counts weeks for every_weeks; set every_weeks to 2 or more, or leave from out")
	}

	// A schedule like 31 February is valid field by field and never runs.
	if _, ok := s.Next(time.Now().In(loc)); !ok {
		return Schedule{}, fmt.Errorf("%q never matches a real date", expr)
	}
	return s, nil
}

// parseField reads one field into a bitmask of the values it allows. It
// fails for a value out of range, an unknown name, a backward range or a
// step of 0.
func parseField(text string, f field) (uint64, error) {
	var mask uint64
	// strings.Split on "," gives each item of a list, or the one item.
	for _, item := range strings.Split(text, ",") {
		if item == "" {
			return 0, fmt.Errorf("%q has an empty item", text)
		}
		// A step follows "/": "*/15" or "0-30/10".
		rng, stepText, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("%q: the step after / must be a whole number of 1 or more", item)
			}
			step = n
		}
		lo, hi := f.min, f.max
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			var err error
			if lo, err = value(a, f); err != nil {
				return 0, err
			}
			hi = lo
			if isRange {
				if hi, err = value(b, f); err != nil {
					return 0, err
				}
				if hi < lo {
					return 0, fmt.Errorf("%q runs backward", rng)
				}
			} else if hasStep {
				// "5/15" means from 5 to the end, every 15, as in other crons.
				hi = f.max
			}
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << v
		}
	}
	return mask, nil
}

// value reads one number or name in field f.
func value(text string, f field) (int, error) {
	for i, name := range f.names {
		if text == name {
			return f.min + i, nil
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%q isn't a number", text)
	}
	if n < f.min {
		return 0, fmt.Errorf("%d is below %d", n, f.min)
	}
	if n > f.max {
		return 0, fmt.Errorf("%d is past %d", n, f.max)
	}
	return n, nil
}

// has reports whether bit v of mask is set.
func has(mask uint64, v int) bool { return mask&(1<<v) != 0 }

// Next returns the first time after t that the schedule matches, in t's
// location, to the minute. It returns false when nothing matches within
// searchYears years.
//
// It moves forward by whole months, days or hours when that field can't
// match, and by minutes only within a matching hour, so a search over a
// year takes a few hundred steps. It builds each candidate with time.Date
// from the wall clock, which handles daylight saving time the way people
// expect: a time that doesn't exist, such as 2:30 when clocks jump from 2:00
// to 3:00, doesn't match that day, and a time that happens twice in the
// autumn matches once.
func (s Schedule) Next(t time.Time) (time.Time, bool) {
	loc := t.Location()
	// The minute after t, with seconds dropped.
	c := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()+1, 0, 0, loc)
	limit := t.AddDate(searchYears, 0, 0)
	for c.Before(limit) {
		// time.Date normalizes overflow: month 13 is January of the next
		// year, and minute 60 is the next hour.
		var next time.Time
		switch {
		case !has(s.month, int(c.Month())):
			next = time.Date(c.Year(), c.Month()+1, 1, 0, 0, 0, 0, loc)
		case !s.dayMatches(c):
			next = time.Date(c.Year(), c.Month(), c.Day()+1, 0, 0, 0, 0, loc)
		case !has(s.hour, c.Hour()):
			next = time.Date(c.Year(), c.Month(), c.Day(), c.Hour()+1, 0, 0, 0, loc)
		case !has(s.minute, c.Minute()):
			next = time.Date(c.Year(), c.Month(), c.Day(), c.Hour(), c.Minute()+1, 0, 0, loc)
		case !c.After(t):
			// In the autumn hour that happens twice, time.Date gives the
			// first one, which may come before t; step past it.
			next = c.Add(time.Minute)
		default:
			return c, true
		}
		// A daylight saving jump can give back the same instant; always
		// move forward.
		if !next.After(c) {
			next = c.Add(time.Minute)
		}
		c = next
	}
	return time.Time{}, false
}

// dayMatches applies cron's day rule and the every-N-weeks rule to c's date.
func (s Schedule) dayMatches(c time.Time) bool {
	dom := has(s.dom, c.Day())
	dow := has(s.dow, int(c.Weekday()))
	var ok bool
	switch {
	case s.domStar && s.dowStar:
		ok = true
	case s.domStar:
		ok = dow
	case s.dowStar:
		ok = dom
	default:
		ok = dom || dow
	}
	if !ok || s.everyWeeks <= 1 {
		return ok
	}
	// from is the first day the job may run; the weeks count from the
	// Monday of its week, so a Thursday from keeps the Tuesday rhythm.
	if daysBetween(s.fromDay, c) < 0 {
		return false
	}
	weeks := daysBetween(s.from, monday(c)) / 7
	return weeks%s.everyWeeks == 0
}

// monday returns the Monday that starts t's week, at midnight in t's
// location.
func monday(t time.Time) time.Time {
	// Weekday counts Sunday as 0; a week here starts on Monday.
	back := (int(t.Weekday()) + 6) % 7
	return time.Date(t.Year(), t.Month(), t.Day()-back, 0, 0, 0, 0, t.Location())
}

// daysBetween counts the calendar days from a to b. It compares dates in
// UTC, so a daylight saving change between them, a 23- or 25-hour day,
// doesn't shift the count.
func daysBetween(a, b time.Time) int {
	da := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(db.Sub(da).Hours() / 24)
}

// Describe writes the schedule in words, such as "7:00 on weekdays" or
// "9:00 on Tuesdays, every other week from 27 October 2026", for the
// clients to show beside the cron expression. A schedule it has no words
// for comes back as the expression, such as "at 0,30 9-17 * * *".
func (s Schedule) Describe() string {
	when, ok := s.timeWords()
	days := s.dayWords()
	if !ok || days == "" {
		return "at " + s.expr
	}
	text := when + " " + days
	if s.text[3] != "*" {
		text += ", in " + s.monthWords()
	}
	switch s.everyWeeks {
	case 0, 1:
	case 2:
		text += ", every other week from " + s.fromDay.Format("2 January 2006")
	default:
		text += fmt.Sprintf(", every %d weeks from %s", s.everyWeeks, s.fromDay.Format("2 January 2006"))
	}
	return text
}

// timeWords says the time of day: "7:00" for one minute in one hour,
// "every 15 minutes" for a step through every hour, "at :30 every hour".
// ok is false for any other shape.
func (s Schedule) timeWords() (string, bool) {
	minutes, hours := bits.OnesCount64(s.minute), bits.OnesCount64(s.hour)
	switch {
	case minutes == 1 && hours == 1:
		return fmt.Sprintf("%d:%02d", lowest(s.hour), lowest(s.minute)), true
	case s.text[1] == "*" && strings.HasPrefix(s.text[0], "*/"):
		return "every " + strings.TrimPrefix(s.text[0], "*/") + " minutes", true
	case s.text[1] == "*" && minutes == 1:
		return fmt.Sprintf("at :%02d every hour", lowest(s.minute)), true
	}
	return "", false
}

// dayWords says which days: "every day", "on weekdays", "on weekends",
// "on Tuesdays", "on Mondays and Thursdays", or "on day 1 of the month".
// It returns "" when both day fields are restricted, which needs cron's
// either-or rule spelled out.
func (s Schedule) dayWords() string {
	weekdays := uint64(0b0111110) // Monday to Friday
	weekend := uint64(0b1000001)  // Sunday and Saturday
	switch {
	case s.domStar && s.dowStar:
		return "every day"
	case s.domStar && s.dow == weekdays:
		return "on weekdays"
	case s.domStar && s.dow == weekend:
		return "on weekends"
	case s.domStar:
		var names []string
		// Monday first, as a week here starts.
		for _, d := range []time.Weekday{1, 2, 3, 4, 5, 6, 0} {
			if has(s.dow, int(d)) {
				names = append(names, d.String()+"s")
			}
		}
		return "on " + joinWords(names)
	case s.dowStar:
		var days []string
		for d := 1; d <= 31; d++ {
			if has(s.dom, d) {
				days = append(days, strconv.Itoa(d))
			}
		}
		if len(days) == 1 {
			return "on day " + days[0] + " of the month"
		}
		return "on days " + joinWords(days) + " of the month"
	}
	return ""
}

// monthWords names the months the schedule allows: "January and July".
func (s Schedule) monthWords() string {
	var names []string
	for m := time.January; m <= time.December; m++ {
		if has(s.month, int(m)) {
			names = append(names, m.String())
		}
	}
	return joinWords(names)
}

// lowest returns the lowest value set in mask.
func lowest(mask uint64) int { return bits.TrailingZeros64(mask) }

// joinWords joins words as a person would: "a", "a and b", "a, b and c".
func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}
