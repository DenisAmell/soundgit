package diff

import (
	"fmt"
	"io"
	"strings"
)

// Text writes changes one per line, in words a musician would use.
func Text(w io.Writer, changes []Change, indent string) {
	for _, c := range changes {
		fmt.Fprintf(w, "%s%s\n", indent, Line(c))
	}
}

// Line renders one change.
func Line(c Change) string {
	switch c.Kind {
	case Added:
		return "+ " + c.Entity
	case Removed:
		return "- " + c.Entity
	case Moved:
		return fmt.Sprintf("> %s: moved from position %v to %v", c.Entity, c.Old, c.New)
	}
	s := "~ " + c.Entity + ": "
	switch {
	case c.Span != nil:
		what := "points"
		if c.Field == "midi" {
			what = "MIDI events"
		}
		if c.Span.Count == 1 {
			what = strings.TrimSuffix(what, "s")
		}
		unit := c.Span.Unit
		if unit == "ticks" {
			unit = " ticks"
		}
		if c.Span.From == c.Span.To {
			s += fmt.Sprintf("%d %s changed at %s%s", c.Span.Count, what, num(c.Span.From), unit)
		} else {
			s += fmt.Sprintf("%d %s changed between %s%s and %s%s",
				c.Span.Count, what, num(c.Span.From), unit, num(c.Span.To), unit)
		}
	case c.Old == nil && c.New == nil:
		s += c.Field
		if c.Detail != "" {
			s += " — " + c.Detail
		}
	default:
		s += fmt.Sprintf("%s %s → %s", c.Field, show(c.Old), show(c.New))
		if c.Detail != "" {
			s += " (" + c.Detail + ")"
		}
	}
	return s
}

func show(v any) string {
	switch x := v.(type) {
	case nil:
		return "(none)"
	case float64:
		return num(x)
	case string:
		return fmt.Sprintf("%q", x)
	case bool:
		if x {
			return "on"
		}
		return "off"
	}
	return fmt.Sprint(v)
}
