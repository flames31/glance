// Package ical parses iCalendar (RFC 5545) documents.
//
// It implements the subset needed to answer "what is happening between these
// two times", not the full specification. Anything it does not understand is
// skipped rather than guessed at: a missing event is recoverable, a wrong one
// sends you to a meeting that is not happening.
package ical

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// Calendar is the set of events parsed from a single .ics document.
type Calendar struct {
	Name   string
	Events []Event
}

// Event is a single VEVENT. Recurring events appear once here; use Occurrences
// to find the times they actually happen.
type Event struct {
	UID      string
	Summary  string
	Location string
	URL      string

	Start time.Time
	End   time.Time

	// AllDay events are date-only in the source. Their End is exclusive, as
	// RFC 5545 defines it: a single-day event ends on the following day.
	AllDay bool

	Recurrence *RecurrenceRule
	// ExDates are occurrences removed from a recurring series.
	ExDates []time.Time

	// RecurrenceID is set when this event overrides a single occurrence of
	// another event in the same series.
	RecurrenceID    time.Time
	HasRecurrenceID bool
}

// Duration of a single occurrence.
func (e Event) Duration() time.Duration {
	if e.End.IsZero() || !e.End.After(e.Start) {
		return 0
	}
	return e.End.Sub(e.Start)
}

var errNoCalendar = errors.New("not an iCalendar document")

// Parse reads an iCalendar document. loc is used for "floating" times, which
// carry neither a Z suffix nor a TZID and are defined to mean local time.
func Parse(r io.Reader, loc *time.Location) (*Calendar, error) {
	if loc == nil {
		loc = time.Local
	}

	lines, err := unfold(r)
	if err != nil {
		return nil, err
	}

	calendar := &Calendar{}
	var current *eventBuilder
	// Depth of components nested inside the VEVENT (VALARM, and anything else
	// a future exporter invents). Properties there are not the event's own.
	nested := 0
	sawCalendar := false

	for _, raw := range lines {
		line, ok := parseContentLine(raw)
		if !ok {
			continue
		}

		switch strings.ToUpper(line.name) {
		case "BEGIN":
			switch strings.ToUpper(line.value) {
			case "VCALENDAR":
				sawCalendar = true
			case "VEVENT":
				if current == nil {
					current = &eventBuilder{loc: loc}
				}
			default:
				if current != nil {
					nested++
				}
			}
			continue

		case "END":
			switch strings.ToUpper(line.value) {
			case "VEVENT":
				if current != nil && nested == 0 {
					if event, ok := current.build(); ok {
						calendar.Events = append(calendar.Events, event)
					}
					current = nil
				}
			default:
				if current != nil && nested > 0 {
					nested--
				}
			}
			continue
		}

		if current != nil {
			if nested == 0 {
				current.set(line)
			}
			continue
		}

		if strings.EqualFold(line.name, "X-WR-CALNAME") {
			calendar.Name = unescapeText(line.value)
		}
	}

	if !sawCalendar {
		return nil, errNoCalendar
	}

	return calendar, nil
}

// unfold reverses RFC 5545 line folding, where a long line is split with a
// line break followed by a single space or tab. This has to happen before any
// other parsing or every long SUMMARY silently truncates.
func unfold(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	// Descriptions with embedded HTML routinely exceed the default 64KB limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var lines []string
	var b strings.Builder

	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")

		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			b.WriteString(line[1:])
			continue
		}

		if b.Len() > 0 {
			lines = append(lines, b.String())
			b.Reset()
		}
		b.WriteString(line)
	}

	if b.Len() > 0 {
		lines = append(lines, b.String())
	}

	return lines, scanner.Err()
}

type contentLine struct {
	name   string
	params map[string]string
	value  string
}

func (l contentLine) param(name string) string {
	return l.params[strings.ToUpper(name)]
}

// parseContentLine splits "NAME;PARAM=VALUE;OTHER=\"quoted:value\":the value".
// The name and parameters end at the first colon that is not inside quotes —
// splitting on the first colon outright breaks on quoted parameter values,
// which real exporters emit for TZIDs containing a colon.
func parseContentLine(raw string) (contentLine, bool) {
	if raw == "" {
		return contentLine{}, false
	}

	quoted := false
	colon := -1
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				colon = i
			}
		}
		if colon >= 0 {
			break
		}
	}

	if colon < 0 {
		return contentLine{}, false
	}

	line := contentLine{value: raw[colon+1:], params: map[string]string{}}
	parts := splitUnquoted(raw[:colon], ';')
	if len(parts) == 0 || parts[0] == "" {
		return contentLine{}, false
	}

	line.name = parts[0]
	for _, part := range parts[1:] {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		line.params[strings.ToUpper(key)] = strings.Trim(value, `"`)
	}

	return line, true
}

func splitUnquoted(s string, sep byte) []string {
	var parts []string
	quoted := false
	start := 0

	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			quoted = !quoted
		case sep:
			if !quoted {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}

	return append(parts, s[start:])
}

// unescapeText reverses the escaping RFC 5545 applies to TEXT values.
func unescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}

		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ';', ',':
			b.WriteByte(s[i])
		default:
			// Not a recognised escape; keep both bytes rather than eating one.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}

	return b.String()
}

type eventBuilder struct {
	loc   *time.Location
	event Event

	cancelled bool
	duration  time.Duration
	hasEnd    bool
}

func (b *eventBuilder) set(line contentLine) {
	switch strings.ToUpper(line.name) {
	case "UID":
		b.event.UID = line.value
	case "SUMMARY":
		b.event.Summary = unescapeText(line.value)
	case "LOCATION":
		b.event.Location = unescapeText(line.value)
	case "URL":
		b.event.URL = line.value
	case "STATUS":
		b.cancelled = strings.EqualFold(line.value, "CANCELLED")

	case "DTSTART":
		if t, allDay, ok := parseDateTime(line, b.loc); ok {
			b.event.Start = t
			b.event.AllDay = allDay
		}

	case "DTEND":
		if t, _, ok := parseDateTime(line, b.loc); ok {
			b.event.End = t
			b.hasEnd = true
		}

	case "DURATION":
		if d, ok := parseDuration(line.value); ok {
			b.duration = d
		}

	case "RRULE":
		b.event.Recurrence = parseRecurrenceRule(line.value, b.loc)

	case "EXDATE":
		for _, value := range strings.Split(line.value, ",") {
			part := contentLine{name: "EXDATE", params: line.params, value: value}
			if t, _, ok := parseDateTime(part, b.loc); ok {
				b.event.ExDates = append(b.event.ExDates, t)
			}
		}

	case "RECURRENCE-ID":
		if t, _, ok := parseDateTime(line, b.loc); ok {
			b.event.RecurrenceID = t
			b.event.HasRecurrenceID = true
		}
	}
}

func (b *eventBuilder) build() (Event, bool) {
	if b.cancelled || b.event.Start.IsZero() {
		return Event{}, false
	}

	if !b.hasEnd {
		switch {
		case b.duration > 0:
			b.event.End = b.event.Start.Add(b.duration)
		case b.event.AllDay:
			// An all-day event with no DTEND covers exactly one day, and DTEND
			// is exclusive, so it lands on the following day.
			b.event.End = b.event.Start.AddDate(0, 0, 1)
		default:
			b.event.End = b.event.Start
		}
	}

	return b.event, true
}

// parseDateTime handles the four forms a DTSTART/DTEND can take:
//
//	;VALUE=DATE:20260904                    all-day
//	:20260904T093000Z                       UTC
//	;TZID=Europe/London:20260904T093000     zoned
//	:20260904T093000                        floating, meaning local time
func parseDateTime(line contentLine, loc *time.Location) (at time.Time, allDay bool, ok bool) {
	value := strings.TrimSpace(line.value)
	if value == "" {
		return time.Time{}, false, false
	}

	if strings.EqualFold(line.param("VALUE"), "DATE") || len(value) == 8 {
		t, err := time.ParseInLocation("20060102", value, loc)
		if err != nil {
			return time.Time{}, false, false
		}
		return t, true, true
	}

	if strings.HasSuffix(value, "Z") {
		t, err := time.ParseInLocation("20060102T150405Z", value, time.UTC)
		if err != nil {
			return time.Time{}, false, false
		}
		return t, false, true
	}

	// A TZID naming a zone Go cannot load (Outlook emits Windows zone names
	// such as "Pacific Standard Time") falls back to the configured location
	// rather than dropping the event.
	zone := loc
	if tzid := line.param("TZID"); tzid != "" {
		if named, err := time.LoadLocation(tzid); err == nil {
			zone = named
		}
	}

	t, err := time.ParseInLocation("20060102T150405", value, zone)
	if err != nil {
		return time.Time{}, false, false
	}

	return t, false, true
}

// parseDuration reads an RFC 5545 duration such as P1DT2H30M or -PT15M.
func parseDuration(s string) (time.Duration, bool) {
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")

	if !strings.HasPrefix(s, "P") {
		return 0, false
	}
	s = s[1:]

	var total time.Duration
	var digits strings.Builder
	inTime := false
	any := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if c >= '0' && c <= '9' {
			digits.WriteByte(c)
			continue
		}

		if c == 'T' {
			inTime = true
			continue
		}

		n, err := strconv.Atoi(digits.String())
		if err != nil {
			return 0, false
		}
		digits.Reset()

		switch c {
		case 'W':
			total += time.Duration(n) * 7 * 24 * time.Hour
		case 'D':
			total += time.Duration(n) * 24 * time.Hour
		case 'H':
			if !inTime {
				return 0, false
			}
			total += time.Duration(n) * time.Hour
		case 'M':
			if !inTime {
				return 0, false
			}
			total += time.Duration(n) * time.Minute
		case 'S':
			if !inTime {
				return 0, false
			}
			total += time.Duration(n) * time.Second
		default:
			return 0, false
		}

		any = true
	}

	if !any {
		return 0, false
	}

	if negative {
		total = -total
	}

	return total, true
}
