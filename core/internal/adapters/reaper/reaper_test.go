package reaper

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DenisAmell/soundgit/core/internal/hashing"
	"github.com/DenisAmell/soundgit/core/internal/ir"
	"github.com/DenisAmell/soundgit/core/internal/testutil"
)

func parseFixture(t *testing.T, name string) *ir.Project {
	t.Helper()
	p, err := Parse(testutil.ReadFixture(t, name))
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return p
}

func canon(t *testing.T, p *ir.Project) string {
	t.Helper()
	b, err := ir.Canonical(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	drums  = "{C0000000-0000-4000-8000-000000000001}"
	kick   = "{C0000000-0000-4000-8000-000000000002}"
	keys   = "{C0000000-0000-4000-8000-000000000003}"
	reverb = "{C0000000-0000-4000-8000-000000000004}"
)

func TestProjectFields(t *testing.T) {
	p := parseFixture(t, "full.rpp")

	if p.Meta.Daw != "reaper" || p.Meta.DawVersion != "7.27/macOS-arm64" || p.Meta.SampleRate != 48000 {
		t.Errorf("meta = %+v", p.Meta)
	}
	if p.Tempo != (ir.Tempo{BPM: 120, Num: 4, Den: 4}) {
		t.Errorf("tempo = %+v", p.Tempo)
	}
	tm := p.TempoMap()
	if tm == nil || len(tm.Points) != 2 || tm.Points[1].Time != 8 || tm.Points[1].Value != 140 {
		t.Fatalf("tempo map = %+v", tm)
	}
	if len(p.Markers) != 3 || p.Markers[0].Name != "Verse" || p.Markers[0].Time != 2 || p.Markers[0].Region {
		t.Errorf("markers = %+v", p.Markers)
	}
	if !p.Markers[1].Region || p.Markers[1].Name != "Chorus `big`" {
		t.Errorf("region marker = %+v", p.Markers[1])
	}
	want := []string{drums, kick, keys, reverb}
	if strings.Join(p.TrackOrder, ",") != strings.Join(want, ",") {
		t.Errorf("track order = %v", p.TrackOrder)
	}
}

func TestTrackFields(t *testing.T) {
	p := parseFixture(t, "full.rpp")

	d := p.Tracks[drums]
	if d.Name != "Drums" || d.FolderDepth != 1 || d.Volume != 1 || d.Mute || d.Phase {
		t.Errorf("drums = %+v", d)
	}
	k := p.Tracks[kick]
	if k.Name != "Kick take" || k.Volume != 0.5 || k.Pan != -0.25 || !k.Phase || k.FolderDepth != -1 || k.Color != 33023 {
		t.Errorf("kick = %+v", k)
	}
	ky := p.Tracks[keys]
	if !ky.Mute || ky.Solo != 2 {
		t.Errorf("keys mute/solo = %v/%d", ky.Mute, ky.Solo)
	}
	r := p.Tracks[reverb]
	if r.Name != "Reverb 'bus'" || len(r.Receives) != 2 {
		t.Fatalf("reverb = %+v", r)
	}
	if r.Receives[0].SrcTrackID != kick || r.Receives[0].Volume != 0.5 {
		t.Errorf("receive 0 = %+v", r.Receives[0])
	}
	if r.Receives[1].SrcTrackID != keys || r.Receives[1].Mode != 3 || r.Receives[1].Pan != 0.1 {
		t.Errorf("receive 1 = %+v", r.Receives[1])
	}
}

func TestFxAndEnvelopes(t *testing.T) {
	p := parseFixture(t, "full.rpp")
	k := p.Tracks[kick]

	if len(k.FxOrder) != 2 {
		t.Fatalf("fx order = %v", k.FxOrder)
	}
	eq := k.Fx["{D0000000-0000-4000-8000-000000000001}"]
	if eq.Format != "VST" || eq.Name != "VST: ReaEQ (Cockos)" || eq.Bypass ||
		eq.Ident != "1919247729<56535472656571726561657100000000>" {
		t.Errorf("eq = %+v", eq)
	}
	if !strings.HasPrefix(eq.State, "cWVlcu5e7f4C") || strings.Count(eq.State, "\n") != 2 {
		t.Errorf("eq state = %q", eq.State)
	}
	if eq.StateHash != hashing.Object("blob", []byte(eq.State)) {
		t.Error("state hash does not match state")
	}
	pe := eq.Envelopes["{A0000000-0000-4000-8000-000000000004}"]
	if pe == nil || pe.Target != "param:2:gain" || len(pe.Points) != 2 {
		t.Errorf("param envelope = %+v", pe)
	}
	js := k.Fx["{D0000000-0000-4000-8000-000000000002}"]
	if js.Format != "JS" || js.Ident != "utility/volume" || !js.Bypass {
		t.Errorf("js = %+v", js)
	}

	vol := k.Envelopes["{A0000000-0000-4000-8000-000000000003}"]
	if vol == nil || vol.Target != "volume" || !vol.Active || len(vol.Points) != 3 {
		t.Fatalf("volume envelope = %+v", vol)
	}
	if vol.Points[1].Time != 4 || vol.Points[1].Value != 0.5 {
		t.Errorf("point = %+v", vol.Points[1])
	}
}

func TestItemsTakesSources(t *testing.T) {
	p := parseFixture(t, "full.rpp")

	it := p.Tracks[kick].Items["{E0000000-0000-4000-8000-000000000001}"]
	if it.Position != 0 || it.Length != 4 || it.Loop || it.FadeIn != 0.01 || it.FadeOut != 0.02 || it.Mute {
		t.Errorf("item = %+v", it)
	}
	if len(it.Takes) != 2 || it.ActiveTake != 1 {
		t.Fatalf("takes = %d active = %d", len(it.Takes), it.ActiveTake)
	}
	t0, t1 := it.Takes[0], it.Takes[1]
	if t0.ID != "{F0000000-0000-4000-8000-000000000001}" || t0.Name != "kick 01.wav" || t0.Offset != 0.25 ||
		t0.Playrate != 1 || t0.Source.Type != "WAVE" || t0.Source.File != "Media/kick 01.wav" {
		t.Errorf("take 0 = %+v source %+v", t0, t0.Source)
	}
	if t1.Pitch != 2 || t1.Source.Type != "SECTION" || t1.Source.Inner == nil ||
		t1.Source.Inner.File != "Media/kick 02.wav" {
		t.Errorf("take 1 = %+v", t1)
	}

	mi := p.Tracks[keys].Items["{E0000000-0000-4000-8000-000000000002}"]
	if !mi.Loop || !mi.Mute || mi.Position != 4 {
		t.Errorf("midi item = %+v", mi)
	}
	m := mi.Takes[0].Source.Midi
	if m == nil || m.PPQ != 960 || len(m.Events) != 6 {
		t.Fatalf("midi = %+v", m)
	}
	if m.Events[0] != "E 0 90 3c 60" || !strings.HasPrefix(m.Events[3], "<X 240 0\n") {
		t.Errorf("events = %q", m.Events)
	}

	got := strings.Join(p.AssetPaths(), ",")
	if got != "Media/kick 01.wav,Media/kick 02.wav" {
		t.Errorf("asset paths = %s", got)
	}
}

func TestDerivedIdentifiersAreStable(t *testing.T) {
	a := parseFixture(t, "track-volume.rpp")
	b := parseFixture(t, "track-volume.rpp")

	if a.TrackOrder[2] != "track:2" {
		t.Errorf("track without id = %q", a.TrackOrder[2])
	}
	guitar := a.Tracks["{C1000000-0000-4000-8000-000000000002}"]
	want := "env:{C1000000-0000-4000-8000-000000000002}:volume"
	if guitar.Envelopes[want] == nil {
		t.Errorf("derived envelope id missing, have %v", guitar.Envelopes)
	}
	if canon(t, a) != canon(t, b) {
		t.Error("two conversions of the same file differ")
	}
}

func TestIdentifierSurvivesRenameAndMove(t *testing.T) {
	src := string(testutil.ReadFixture(t, "track-volume.rpp"))
	bass := src[strings.Index(src, "  <TRACK {C1000000-0000-4000-8000-000000000001}"):strings.Index(src, "  <TRACK {C1000000-0000-4000-8000-000000000002}")]
	moved := strings.Replace(src, bass, "", 1)
	moved = strings.Replace(moved, "  <TRACK\n", strings.Replace(bass, "NAME Bass", `NAME "Sub Bass"`, 1)+"  <TRACK\n", 1)

	p, err := Parse([]byte(moved))
	if err != nil {
		t.Fatal(err)
	}
	const id = "{C1000000-0000-4000-8000-000000000001}"
	if p.TrackOrder[1] != id || p.Tracks[id].Name != "Sub Bass" {
		t.Errorf("order = %v, name = %q", p.TrackOrder, p.Tracks[id].Name)
	}
}

func TestDuplicateIdentifierIsAnError(t *testing.T) {
	src := string(testutil.ReadFixture(t, "track-volume.rpp"))
	src = strings.ReplaceAll(src, "C1000000-0000-4000-8000-000000000002", "C1000000-0000-4000-8000-000000000001")
	_, err := Parse([]byte(src))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"duplicate track id", "track #1", "track #2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestPathNormalization(t *testing.T) {
	p := parseFixture(t, "windows-paths.rpp")
	got := p.AssetPaths()
	if len(got) != 2 || got[0] != "Media/take 01.wav" || got[1] != "C:/Samples/external.wav" {
		t.Fatalf("asset paths = %v", got)
	}
	if ir.IsExternalPath(got[0]) || !ir.IsExternalPath(got[1]) {
		t.Error("external path detection is wrong")
	}
}

// File → model → file must give back the same model, and for canonically
// formatted files the same bytes.
func TestWriteRoundTripCorpus(t *testing.T) {
	for _, path := range testutil.Corpus(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			p1, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			out := Write(p1)
			p2, err := Parse(out)
			if err != nil {
				t.Fatalf("written file does not parse: %v", err)
			}
			if canon(t, p1) != canon(t, p2) {
				t.Fatal("model changed after writing and reading back")
			}
			if !bytes.Equal(out, data) {
				t.Errorf("written file differs from the source:\n%s", firstDiff(string(data), string(out)))
			}
		})
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d\n  source:  %s\n  written: %s", i+1, al[i], bl[i])
		}
	}
	return "one file is a prefix of the other"
}

func TestChangingOneFieldTouchesOneLine(t *testing.T) {
	data := testutil.ReadFixture(t, "full.rpp")
	p, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	p.Tracks[kick].Name = "Kick `main` \"A\""
	out := string(Write(p))

	want := strings.Replace(string(data), `NAME "Kick take"`, "NAME 'Kick `main` \"A\"'", 1)
	if out != want {
		t.Fatalf("unexpected output:\n%s", firstDiff(want, out))
	}
	back, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if back.Tracks[kick].Name != p.Tracks[kick].Name {
		t.Errorf("name read back as %q", back.Tracks[kick].Name)
	}
}

func TestModelEditsReachTheFile(t *testing.T) {
	data := string(testutil.ReadFixture(t, "full.rpp"))
	p, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p.Tempo.BPM = 128
	p.Tracks[kick].Volume = 0.25
	p.Tracks[kick].Envelopes["{A0000000-0000-4000-8000-000000000003}"].Points[1].Value = 0.75
	it := p.Tracks[kick].Items["{E0000000-0000-4000-8000-000000000001}"]
	it.Position = 1.5
	it.ActiveTake = 0
	// Move the last track to the front: receives must follow their sources.
	p.TrackOrder = []string{reverb, drums, kick, keys}

	out := string(Write(p))
	for _, want := range []string{
		"  TEMPO 128 4 4\n",
		"    VOLPAN 0.25 -0.25 -1 -1 1\n",
		"      PT 4 0.75 0\n",
		"      POSITION 1.5\n",
		"      TAKE\n",
		"    AUXRECV 2 0 0.5 0 0 0 0 0 0 -1:U 0 -1 ''\n",
		"    AUXRECV 3 3 1 0.1 0 0 0 0 0 -1:U 0 -1 ''\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if strings.Contains(out, "TAKE SEL") {
		t.Error("TAKE SEL should be gone")
	}

	back, err := Parse([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if back.TrackOrder[0] != reverb || back.Tracks[reverb].Receives[0].SrcTrackID != kick {
		t.Errorf("order = %v, receive source = %s", back.TrackOrder, back.Tracks[reverb].Receives[0].SrcTrackID)
	}
	if back.Tempo.BPM != 128 || back.Tracks[kick].Volume != 0.25 {
		t.Error("edits were not read back")
	}
}

// Golden digest: must be identical on macOS, Windows and Linux.
func TestCanonicalSerializationIsDeterministic(t *testing.T) {
	p := parseFixture(t, "full.rpp")
	first := canon(t, p)
	for i := 0; i < 5; i++ {
		if canon(t, parseFixture(t, "full.rpp")) != first {
			t.Fatal("serialization differs between runs")
		}
	}
	const golden = "3ba0ed13581fdd1775be9d03e585cf06af127955a11153fff2d98b5cb3a2d3f2"
	if got := hashing.Object("test", []byte(first)); got != golden {
		t.Errorf("digest of full.rpp model = %s, want %s", got, golden)
	}
}
