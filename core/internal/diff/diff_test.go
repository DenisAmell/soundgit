package diff

import (
	"strings"
	"testing"

	"github.com/DenisAmell/soundgit/core/internal/adapters/reaper"
	"github.com/DenisAmell/soundgit/core/internal/ir"
	"github.com/DenisAmell/soundgit/core/internal/testutil"
)

const (
	kick   = "{C0000000-0000-4000-8000-000000000002}"
	reverb = "{C0000000-0000-4000-8000-000000000004}"
)

func parse(t *testing.T, src string) *ir.Project {
	t.Helper()
	p, err := reaper.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// edited returns the base project and a copy with every old→new pair applied
// to the file text, the way a save from the DAW would change it.
func edited(t *testing.T, pairs ...string) (*ir.Project, *ir.Project) {
	t.Helper()
	src := string(testutil.ReadFixture(t, "full.rpp"))
	out := src
	for i := 0; i < len(pairs); i += 2 {
		if !strings.Contains(out, pairs[i]) {
			t.Fatalf("fixture lacks %q", pairs[i])
		}
		out = strings.Replace(out, pairs[i], pairs[i+1], 1)
	}
	return parse(t, src), parse(t, out)
}

func lines(cs []Change) string {
	var b strings.Builder
	Text(&b, cs, "")
	return b.String()
}

func TestIdenticalVersions(t *testing.T) {
	a, b := edited(t)
	if cs := Projects(a, b, true); len(cs) != 0 {
		t.Fatalf("expected no changes, got:\n%s", lines(cs))
	}
}

func TestTempoChangeAndAddedTrack(t *testing.T) {
	newTrack := `  <TRACK {C0000000-0000-4000-8000-000000000009}
    NAME Vocal
    VOLPAN 1 0 -1 -1 1
    TRACKID {C0000000-0000-4000-8000-000000000009}
    <ITEM
      POSITION 0
      LENGTH 1
      IGUID {E0000000-0000-4000-8000-000000000009}
    >
  >
>
`
	src := string(testutil.ReadFixture(t, "full.rpp"))
	out := strings.Replace(src, "  TEMPO 120 4 4\n", "  TEMPO 128 4 4\n", 1)
	out = strings.TrimSuffix(out, ">\n") + newTrack

	cs := Projects(parse(t, src), parse(t, out), false)
	want := "~ project: tempo 120 → 128\n+ track \"Vocal\"\n"
	if got := lines(cs); got != want {
		t.Fatalf("got:\n%swant:\n%s", got, want)
	}
	if cs[0].Old != 120.0 || cs[0].New != 128.0 {
		t.Errorf("tempo change carries %v → %v", cs[0].Old, cs[0].New)
	}
}

func TestRemovedTrack(t *testing.T) {
	a, b := edited(t)
	delete(b.Tracks, reverb)
	b.TrackOrder = b.TrackOrder[:3]
	if got := lines(Projects(a, b, false)); got != "- track \"Reverb 'bus'\"\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestMovedTrackIsOneChange(t *testing.T) {
	a, b := edited(t)
	o := b.TrackOrder
	b.TrackOrder = []string{o[3], o[0], o[1], o[2]}
	cs := Projects(a, b, false)
	if len(cs) != 1 || cs[0].Kind != Moved || cs[0].ID != reverb || cs[0].Old != 4 || cs[0].New != 1 {
		t.Fatalf("got:\n%s", lines(cs))
	}
}

func TestMixerFields(t *testing.T) {
	a, b := edited(t,
		"    VOLPAN 0.5 -0.25 -1 -1 1\n", "    VOLPAN 1 0.5 -1 -1 1\n",
		"    MUTESOLO 1 2 0\n", "    MUTESOLO 0 0 0\n",
		`    NAME "Kick take"`, `    NAME "Kick"`,
	)
	got := lines(Projects(a, b, false))
	for _, want := range []string{
		`~ track "Kick": name "Kick take" → "Kick"`,
		`~ track "Kick": volume 0.5 → 1 (-6.0 dB → +0.0 dB)`,
		`~ track "Kick": pan -0.25 → 0.5`,
		`~ track "Keys": mute on → off`,
		`~ track "Keys": solo 2 → 0`,
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 5 {
		t.Errorf("expected 5 changes, got %d:\n%s", n, got)
	}
}

func TestItemAndTake(t *testing.T) {
	a, b := edited(t,
		"      POSITION 0\n      SNAPOFFS 0\n      LENGTH 4\n", "      POSITION 1.5\n      SNAPOFFS 0\n      LENGTH 3\n",
		"      TAKE SEL\n", "      TAKE\n",
		`        FILE "Media/kick 01.wav"`, `        FILE "Media/kick 01 edit.wav"`,
	)
	got := lines(Projects(a, b, false))
	for _, want := range []string{
		`item "kick 01.wav" at 1.5s: position 0 → 1.5`,
		`at 1.5s: length 4 → 3`,
		`at 1.5s: activeTake 2 → 1`,
		`take "kick 01.wav": file "Media/kick 01.wav" → "Media/kick 01 edit.wav"`,
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestFxChanges(t *testing.T) {
	a, b := edited(t,
		"      BYPASS 0 0 0\n      <VST", "      BYPASS 1 0 0\n      <VST",
		"        AAAQAAAA\n", "        AAAQBBBB\n",
	)
	got := lines(Projects(a, b, false))
	want := "~ track \"Kick take\" / fx \"VST: ReaEQ (Cockos)\": bypass off → on\n" +
		"~ track \"Kick take\" / fx \"VST: ReaEQ (Cockos)\": state — plugin settings changed\n"
	if got != want {
		t.Fatalf("got:\n%swant:\n%s", got, want)
	}

	// Swapping the two effects is a move, and removing one is a removal.
	a, b = edited(t)
	k := b.Tracks[kick]
	k.FxOrder = []string{k.FxOrder[1], k.FxOrder[0]}
	cs := Projects(a, b, false)
	if len(cs) != 1 || cs[0].Kind != Moved || cs[0].Type != "fx" {
		t.Fatalf("got:\n%s", lines(cs))
	}
	a, b = edited(t)
	k = b.Tracks[kick]
	delete(k.Fx, k.FxOrder[1])
	k.FxOrder = k.FxOrder[:1]
	if got := lines(Projects(a, b, false)); got != "- track \"Kick take\" / fx \"utility/volume\"\n" {
		t.Fatalf("got:\n%s", got)
	}
}

func TestAutomationEditReportsCountAndInterval(t *testing.T) {
	a, b := edited(t,
		"      PT 4 0.5 0\n      PT 8 1 0 0 1\n", "      PT 4 0.25 0\n      PT 6 0.8 0\n      PT 8 1 0 0 1\n",
	)
	cs := Projects(a, b, false)
	if len(cs) != 1 {
		t.Fatalf("got:\n%s", lines(cs))
	}
	c := cs[0]
	if c.Type != "envelope" || c.ID != "{A0000000-0000-4000-8000-000000000003}" || c.Span == nil ||
		c.Span.Count != 2 || c.Span.From != 4 || c.Span.To != 6 || c.Span.Unit != "s" {
		t.Fatalf("change = %+v span = %+v", c, c.Span)
	}
	want := `~ track "Kick take" / volume automation: 2 points changed between 4s and 6s`
	if Line(c) != want {
		t.Errorf("line = %q", Line(c))
	}
}

func TestTempoMapAndMidi(t *testing.T) {
	a, b := edited(t,
		"    PT 8.000000000000 140.0000000000 1 0 0 1\n", "    PT 8.000000000000 150.0000000000 1 0 0 1\n",
		"        E 480 80 3c 00\n", "        E 240 80 3c 00\n",
	)
	got := lines(Projects(a, b, false))
	for _, want := range []string{
		"~ project / tempo automation: 1 point changed at 8s",
		// Shortening the first note shifts every later event.
		"5 MIDI events changed between 240 ticks and 1920 ticks",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestUnrecognizedAndViewData(t *testing.T) {
	a, b := edited(t,
		"  CURSOR 4.5\n", "  CURSOR 12\n",
		"  PANLAW 1\n", "  PANLAW 0.5\n",
		"    SEL 1\n", "    SEL 0\n",
	)
	got := lines(Projects(a, b, false))
	if got != "~ project: other:PANLAW \"PANLAW 1\" → \"PANLAW 0.5\" (unrecognized data changed)\n" {
		t.Fatalf("without view state, got:\n%s", got)
	}
	if n := len(Projects(a, b, true)); n != 3 {
		t.Fatalf("with view state expected 3 changes, got %d", n)
	}
}

func TestNumberSpellingIsNotAChange(t *testing.T) {
	a, b := edited(t,
		"    VOLPAN 0.5 -0.25 -1 -1 1\n", "    VOLPAN 0.50000 -0.250 -1 -1 1\n",
		"  TEMPO 120 4 4\n", "  TEMPO 120.0 4 4\n",
		"      PT 4 0.5 0\n", "      PT 4.000 0.5 0\n",
	)
	if cs := Projects(a, b, true); len(cs) != 0 {
		t.Fatalf("expected no changes, got:\n%s", lines(cs))
	}
}
