package ical

import (
	"strconv"
	"strings"
	"time"
)

// RecurrenceRule is the subset of RRULE this package understands.
//
// Rules using anything outside that subset are marked unsupported and their
// event contributes only its original occurrence, rather than a series
// generated from a rule we have guessed at.
type RecurrenceRule struct {
	Freq     string // DAILY, WEEKLY, MONTHLY or YEARLY
	Interval int
	Count    int
	Until    time.Time
	HasUntil bool
	// ByDay holds simple weekday names only. Ordinals such as "2MO" mark the
	// rule unsupported instead.
	ByDay []time.Weekday

	Supported bool
}

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday,
	"MO": time.Monday,
	"TU": time.Tuesday,
	"WE": time.Wednesday,
	"TH": time.Thursday,
	"FR": time.Friday,
	"SA": time.Saturday,
}

// Parts that change which dates a rule produces in ways this package does not
// model. Seeing any of them makes the rule unsupported.
var unsupportedRuleParts = []string{
	"BYSETPOS", "BYWEEKNO", "BYYEARDAY", "BYMONTHDAY", "BYMONTH", "BYHOUR", "BYMINUTE", "BYSECOND",
}

func parseRecurrenceRule(value string, loc *time.Location) *RecurrenceRule {
	rule := &RecurrenceRule{Interval: 1, Supported: true}

	for _, part := range strings.Split(value, ";") {
		key, val, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch key {
		case "FREQ":
			rule.Freq = strings.ToUpper(val)

		case "INTERVAL":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				rule.Supported = false
				continue
			}
			rule.Interval = n

		case "COUNT":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				rule.Supported = false
				continue
			}
			rule.Count = n

		case "UNTIL":
			line := contentLine{name: "UNTIL", params: map[string]string{}, value: val}
			if t, _, ok := parseDateTime(line, loc); ok {
				rule.Until = t
				rule.HasUntil = true
			} else {
				rule.Supported = false
			}

		case "BYDAY":
			for _, day := range strings.Split(val, ",") {
				day = strings.ToUpper(strings.TrimSpace(day))
				weekday, ok := weekdays[day]
				if !ok {
					// Either an ordinal like "2MO" or something unrecognised.
					rule.Supported = false
					continue
				}
				rule.ByDay = append(rule.ByDay, weekday)
			}

		case "WKST":
			// Only affects rules with BYWEEKNO or an interval over weeks, both
			// of which are already unsupported. Safe to ignore.

		default:
			for _, unsupported := range unsupportedRuleParts {
				if key == unsupported {
					rule.Supported = false
				}
			}
		}
	}

	switch rule.Freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		// Includes HOURLY/MINUTELY/SECONDLY, which no calendar UI produces.
		rule.Supported = false
	}

	// BYDAY only means "these weekdays" under a WEEKLY rule. Under MONTHLY or
	// YEARLY it selects e.g. "the first Monday", which needs BYSETPOS-style
	// handling we do not implement.
	if len(rule.ByDay) > 0 && rule.Freq != "WEEKLY" {
		rule.Supported = false
	}

	return rule
}

// Guards against a malformed rule spinning forever. A COUNT-limited series has
// to be walked from the start, so the cap has to be generous.
const maxRecurrenceSteps = 100_000

// Occurrences returns the start times at which this event happens within
// [from, to). An occurrence is included when any part of it overlaps the
// window, so a meeting already in progress still counts as happening now.
func (e Event) Occurrences(from, to time.Time) []time.Time {
	if !to.After(from) {
		return nil
	}

	duration := e.Duration()
	overlaps := func(start time.Time) bool {
		return start.Before(to) && start.Add(duration).After(from)
	}

	if e.Recurrence == nil || !e.Recurrence.Supported {
		if overlaps(e.Start) {
			return []time.Time{e.Start}
		}
		return nil
	}

	var found []time.Time
	for _, start := range e.recurrenceStarts(from, to) {
		if e.isExcluded(start) {
			continue
		}
		if overlaps(start) {
			found = append(found, start)
		}
	}

	return found
}

func (e Event) isExcluded(start time.Time) bool {
	for _, excluded := range e.ExDates {
		if excluded.Equal(start) {
			return true
		}
	}
	return false
}

// recurrenceStarts walks the series far enough to cover the window.
func (e Event) recurrenceStarts(from, to time.Time) []time.Time {
	rule := e.Recurrence

	if rule.Freq == "WEEKLY" && len(rule.ByDay) > 0 {
		return e.weeklyByDayStarts(from, to)
	}

	step := func(t time.Time, periods int) time.Time {
		switch rule.Freq {
		case "DAILY":
			return atClockTime(t.AddDate(0, 0, periods*rule.Interval), e.Start)
		case "WEEKLY":
			return atClockTime(t.AddDate(0, 0, 7*periods*rule.Interval), e.Start)
		case "MONTHLY":
			return atClockTime(t.AddDate(0, periods*rule.Interval, 0), e.Start)
		default: // YEARLY
			return atClockTime(t.AddDate(periods*rule.Interval, 0, 0), e.Start)
		}
	}

	current := e.Start
	index := 0

	// With no COUNT the index does not matter, so skip straight to the window
	// instead of walking years of history one period at a time.
	if rule.Count == 0 {
		if skip := e.periodsBefore(from); skip > 0 {
			current = step(e.Start, skip)
			index = skip
		}
	}

	var starts []time.Time
	for steps := 0; steps < maxRecurrenceSteps; steps++ {
		if rule.Count > 0 && index >= rule.Count {
			break
		}
		if rule.HasUntil && current.After(rule.Until) {
			break
		}
		if !current.Before(to) {
			break
		}

		// AddDate normalises overflow, turning 31 January plus one month into
		// 2 or 3 March. RFC 5545 says such dates are skipped, so drop any
		// occurrence that landed on a different day of the month.
		if rule.Freq != "MONTHLY" || current.Day() == e.Start.Day() {
			starts = append(starts, current)
		}

		index++
		current = step(e.Start, index)
	}

	return starts
}

// periodsBefore estimates how many whole periods separate the event's start
// from the window, so the walk can begin near the window rather than at the
// beginning of the series. Deliberately conservative: it under-estimates by a
// period so the caller never skips past a live occurrence.
func (e Event) periodsBefore(from time.Time) int {
	if !from.After(e.Start) {
		return 0
	}

	rule := e.Recurrence
	var periods int

	switch rule.Freq {
	case "DAILY":
		periods = int(from.Sub(e.Start).Hours()/24) / rule.Interval
	case "WEEKLY":
		periods = int(from.Sub(e.Start).Hours()/(24*7)) / rule.Interval
	case "MONTHLY":
		months := (from.Year()-e.Start.Year())*12 + int(from.Month()) - int(e.Start.Month())
		periods = months / rule.Interval
	default: // YEARLY
		periods = (from.Year() - e.Start.Year()) / rule.Interval
	}

	if periods < 1 {
		return 0
	}

	return periods - 1
}

// weeklyByDayStarts expands "every other Monday and Wednesday" style rules.
// Occurrences are anchored to the week containing DTSTART.
func (e Event) weeklyByDayStarts(from, to time.Time) []time.Time {
	rule := e.Recurrence
	weekStart := startOfWeek(e.Start)

	week := 0
	if rule.Count == 0 && from.After(e.Start) {
		if elapsed := int(from.Sub(weekStart).Hours() / (24 * 7)); elapsed > rule.Interval {
			week = (elapsed / rule.Interval) - 1
		}
	}

	var starts []time.Time
	index := week * len(rule.ByDay)

	for steps := 0; steps < maxRecurrenceSteps; steps++ {
		base := weekStart.AddDate(0, 0, 7*rule.Interval*week)
		if !base.Before(to) {
			break
		}

		for _, weekday := range rule.ByDay {
			offset := (int(weekday) - int(time.Monday) + 7) % 7
			start := atClockTime(base.AddDate(0, 0, offset), e.Start)

			// The rule cannot produce anything before the event itself.
			if start.Before(e.Start) {
				continue
			}
			if rule.Count > 0 && index >= rule.Count {
				return starts
			}
			if rule.HasUntil && start.After(rule.Until) {
				return starts
			}

			index++
			if start.Before(to) {
				starts = append(starts, start)
			}
		}

		week++
	}

	return starts
}

// atClockTime puts ref's wall-clock time onto day's date. Recurring events keep
// their local start time across a daylight-saving change, so the clock time is
// reapplied rather than a fixed duration being added.
func atClockTime(day time.Time, ref time.Time) time.Time {
	year, month, date := day.Date()
	hour, minute, second := ref.Clock()

	return time.Date(year, month, date, hour, minute, second, 0, ref.Location())
}

func startOfWeek(t time.Time) time.Time {
	offset := (int(t.Weekday()) - int(time.Monday) + 7) % 7
	year, month, date := t.AddDate(0, 0, -offset).Date()

	return time.Date(year, month, date, 0, 0, 0, 0, t.Location())
}
