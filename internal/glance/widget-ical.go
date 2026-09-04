package glance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/glanceapp/glance/pkg/ical"
)

var icalWidgetTemplate = mustParseTemplate("ical.html", "widget-base.html")

type icalWidget struct {
	widgetBase `yaml:",inline"`

	Calendars  []icalCalendar `yaml:"calendars"`
	Days       int            `yaml:"days"`
	Limit      int            `yaml:"limit"`
	Timezone   string         `yaml:"timezone"`
	HourFormat string         `yaml:"hour-format"`

	Agenda []icalDay `yaml:"-"`

	location   *time.Location
	timeFormat string
}

type icalCalendar struct {
	URL     string            `yaml:"url"`
	Name    string            `yaml:"name"`
	Headers map[string]string `yaml:"headers"`
}

// icalDay is one heading in the agenda, with the events falling under it.
type icalDay struct {
	Label  string
	Events []icalEvent
}

type icalEvent struct {
	Title    string
	Location string
	URL      string
	Calendar string
	// TimeLabel is precomputed because there is no time-formatting function
	// available to templates.
	TimeLabel string
	AllDay    bool
	Start     time.Time
}

func (widget *icalWidget) initialize() error {
	widget.withTitle("Calendar").withCacheDuration(1 * time.Hour)

	if len(widget.Calendars) == 0 {
		return errors.New("at least one calendar must be configured")
	}
	for i := range widget.Calendars {
		if widget.Calendars[i].URL == "" {
			return fmt.Errorf("calendar %d has no url", i+1)
		}
		widget.Calendars[i].URL = normalizeCalendarURL(widget.Calendars[i].URL)
	}

	if widget.Days <= 0 {
		widget.Days = 1
	}
	if widget.Limit <= 0 {
		widget.Limit = 10
	}

	switch widget.HourFormat {
	case "":
		widget.HourFormat = "24h"
	case "12h", "24h":
	default:
		return errors.New("hour-format must be either 12h or 24h")
	}

	// Which events count as "today" has to be decided server-side in order to
	// filter, so follow the weather widget and take an explicit timezone.
	widget.location = time.Local
	if widget.Timezone != "" {
		location, err := time.LoadLocation(widget.Timezone)
		if err != nil {
			return fmt.Errorf("invalid timezone '%s': %v", widget.Timezone, err)
		}
		widget.location = location
	}

	widget.timeFormat = "15:04"
	if widget.HourFormat == "12h" {
		widget.timeFormat = "3:04pm"
	}

	return nil
}

func (widget *icalWidget) update(ctx context.Context) {
	days, err := widget.fetchEvents(ctx)
	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	widget.Agenda = days
}

func (widget *icalWidget) Render() template.HTML {
	return widget.renderTemplate(widget, icalWidgetTemplate)
}

func (widget *icalWidget) fetchEvents(ctx context.Context) ([]icalDay, error) {
	job := newJob(widget.fetchCalendarTask(ctx), widget.Calendars).withWorkers(10)
	results, errs, err := workerPoolDo(job)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoContent, err)
	}

	now := time.Now().In(widget.location)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, widget.location)
	to := from.AddDate(0, 0, widget.Days)

	var events []icalEvent
	failed := 0

	for i := range results {
		if errs[i] != nil {
			failed++
			slog.Error("Failed to get calendar", "url", widget.Calendars[i].URL, "error", errs[i])
			continue
		}

		name := widget.Calendars[i].Name
		if name == "" {
			name = results[i].Name
		}

		events = append(events, widget.eventsInWindow(results[i], name, from, to)...)
	}

	if failed == len(widget.Calendars) {
		return nil, errNoContent
	}

	sort.Slice(events, func(a, b int) bool {
		// All-day events belong at the top of their day rather than at
		// midnight amongst the timed ones.
		if events[a].Start.Equal(events[b].Start) {
			return events[a].AllDay && !events[b].AllDay
		}
		return events[a].Start.Before(events[b].Start)
	})

	if len(events) > widget.Limit {
		events = events[:widget.Limit]
	}

	days := groupEventsByDay(events, from)

	if failed > 0 {
		return days, fmt.Errorf("%w: missing %d calendars", errPartialContent, failed)
	}

	return days, nil
}

// eventsInWindow expands each event's occurrences and flattens them into the
// display model. An event recurring twice in the window yields two entries.
func (widget *icalWidget) eventsInWindow(
	calendar *ical.Calendar,
	name string,
	from, to time.Time,
) []icalEvent {
	// A RECURRENCE-ID event replaces one occurrence of its series, so the
	// generated occurrence at that time must be suppressed.
	overridden := make(map[string]struct{})
	for _, event := range calendar.Events {
		if event.HasRecurrenceID {
			overridden[event.UID+"@"+event.RecurrenceID.UTC().Format(time.RFC3339)] = struct{}{}
		}
	}

	var events []icalEvent

	for _, event := range calendar.Events {
		for _, start := range event.Occurrences(from, to) {
			if !event.HasRecurrenceID {
				key := event.UID + "@" + start.UTC().Format(time.RFC3339)
				if _, ok := overridden[key]; ok {
					continue
				}
			}

			local := start.In(widget.location)
			title := event.Summary
			if title == "" {
				title = "(no title)"
			}

			label := local.Format(widget.timeFormat)
			if event.AllDay {
				label = "all-day"
			}

			events = append(events, icalEvent{
				Title:     title,
				Location:  event.Location,
				URL:       event.URL,
				Calendar:  name,
				TimeLabel: label,
				AllDay:    event.AllDay,
				Start:     local,
			})
		}
	}

	return events
}

func groupEventsByDay(events []icalEvent, today time.Time) []icalDay {
	var days []icalDay

	for _, event := range events {
		label := relativeDayLabel(event.Start, today)

		if len(days) == 0 || days[len(days)-1].Label != label {
			days = append(days, icalDay{Label: label})
		}

		days[len(days)-1].Events = append(days[len(days)-1].Events, event)
	}

	return days
}

// relativeDayLabel names a day the way a person would, falling back to a date
// once "tomorrow" stops being meaningful.
func relativeDayLabel(t time.Time, today time.Time) string {
	switch daysBetween(today, t) {
	case 0:
		return "Today"
	case 1:
		return "Tomorrow"
	case 2, 3, 4, 5, 6:
		return t.Format("Monday")
	default:
		return t.Format("Mon 2 Jan")
	}
}

// daysBetween counts calendar days, not 24-hour periods. time.Truncate operates
// on absolute time and so lands mid-day in any zone with an offset.
func daysBetween(from, to time.Time) int {
	y1, m1, d1 := from.Date()
	y2, m2, d2 := to.Date()

	start := time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)
	end := time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC)

	return int(end.Sub(start).Hours() / 24)
}

// normalizeCalendarURL rewrites the webcal scheme that calendar applications
// hand out for subscription links. It is a convention meaning "fetch this ICS
// over HTTP", not a transport Go's http client knows about, so leaving it
// alone fails with "unsupported protocol scheme".
func normalizeCalendarURL(url string) string {
	// webcals is the explicitly-TLS spelling; plain webcal is also served over
	// HTTPS by every provider that publishes it.
	for _, scheme := range []string{"webcals://", "webcal://"} {
		if len(url) >= len(scheme) && strings.EqualFold(url[:len(scheme)], scheme) {
			return "https://" + url[len(scheme):]
		}
	}

	return url
}

func (widget *icalWidget) fetchCalendarTask(ctx context.Context) func(icalCalendar) (*ical.Calendar, error) {
	return func(calendar icalCalendar) (*ical.Calendar, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, calendar.URL, nil)
		if err != nil {
			return nil, err
		}

		request.Header.Set("User-Agent", glanceUserAgentString)
		for key, value := range calendar.Headers {
			request.Header.Set(key, value)
		}

		response, err := defaultHTTPClient.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()

		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code %d", response.StatusCode)
		}

		body, err := io.ReadAll(response.Body)
		if err != nil {
			return nil, err
		}

		return ical.Parse(bytes.NewReader(body), widget.location)
	}
}
