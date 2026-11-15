package reaper

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/diff"
	"github.com/DenisAmell/soundgit/core/internal/ir"
	"github.com/DenisAmell/soundgit/core/internal/testutil"
)

func realProject(t *testing.T, name string) (*ir.Project, string) {
	t.Helper()
	path := filepath.Join(testutil.Root(t), "testdata", "rpp", "minimal", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no minimal corpus: %v", err)
	}
	p, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return p, path
}

func trackByName(t *testing.T, p *ir.Project, name string) *ir.Track {
	t.Helper()
	for _, id := range p.TrackOrder {
		if p.Tracks[id].Name == name {
			return p.Tracks[id]
		}
	}
	t.Fatalf("no track %q", name)
	return nil
}

// A project saved by Reaper itself, read into the model.
func TestProjectSavedByReaper(t *testing.T) {
	p, _ := realProject(t, "20-sends.rpp")

	var names []string
	for _, id := range p.TrackOrder {
		names = append(names, p.Tracks[id].Name)
	}
	want := "Bass|Drums bus|Kick \"in\"|Snare 'top' \"x\"|Keys|Reverb"
	if got := strings.Join(names, "|"); got != want {
		t.Errorf("tracks = %s", got)
	}
	if p.Tempo != (ir.Tempo{BPM: 128, Num: 4, Den: 4}) {
		t.Errorf("tempo = %+v", p.Tempo)
	}
	if tm := p.TempoMap(); tm == nil || len(tm.Points) != 2 || tm.Points[1].Value != 140 {
		t.Errorf("tempo map = %+v", tm)
	}
	if len(p.Markers) != 3 || p.Markers[0].Name != "Verse" || !p.Markers[1].Region || p.Markers[1].Name != "Chorus `big`" || p.Markers[2].Time != 16 {
		t.Errorf("markers = %+v", p.Markers)
	}

	bass := trackByName(t, p, "Bass")
	if bass.Pan != -0.25 || !bass.Phase || bass.Mute {
		t.Errorf("bass = %+v", bass)
	}
	if len(bass.FxOrder) != 2 || bass.Fx[bass.FxOrder[0]].Format != "JS" || !bass.Fx[bass.FxOrder[0]].Bypass ||
		bass.Fx[bass.FxOrder[1]].Name != "VST: ReaEQ (Cockos)" || bass.Fx[bass.FxOrder[1]].State == "" {
		t.Errorf("bass fx = %v", bass.FxOrder)
	}
	var vol *ir.Envelope
	for _, e := range bass.Envelopes {
		if e.Target == "volume" {
			vol = e
		}
	}
	if vol == nil || !vol.Active || len(vol.Points) != 4 {
		t.Errorf("bass volume envelope = %+v", vol)
	}

	if d, s := trackByName(t, p, "Drums bus"), trackByName(t, p, "Snare 'top' \"x\""); d.FolderDepth != 1 || s.FolderDepth != -1 {
		t.Errorf("folder depths = %d, %d", d.FolderDepth, s.FolderDepth)
	}

	kick := trackByName(t, p, `Kick "in"`)
	it := kick.Items[kick.ItemOrder[0]]
	if len(it.Takes) != 2 || it.ActiveTake != 1 || it.Takes[0].Source.File != "Media/sine 440.wav" ||
		it.Takes[1].Name != "second take" || it.Takes[1].Source.File != "Media/sine 660.wav" {
		t.Errorf("kick item = %+v", it)
	}
	keys := trackByName(t, p, "Keys")
	if m := keys.Items[keys.ItemOrder[0]].Takes[0].Source.Midi; m == nil || m.PPQ != 960 || len(m.Events) != 7 {
		t.Errorf("midi = %+v", m)
	}

	rev := trackByName(t, p, "Reverb")
	if len(rev.Receives) != 2 || rev.Receives[0].SrcTrackID != kick.ID || rev.Receives[1].SrcTrackID != keys.ID ||
		rev.Receives[0].Volume != 0.5 {
		t.Errorf("receives = %+v", rev.Receives)
	}
}

// Reaper regenerates ids when it duplicates, and keeps them through rename,
// move and "save as" — which is what merging by id relies on.
func TestReaperIdentifierBehaviour(t *testing.T) {
	before, _ := realProject(t, "22-resave.rpp")
	dup, _ := realProject(t, "23-guid-duplicate-track.rpp")
	after, _ := realProject(t, "25-guid-rename-move.rpp")

	if len(dup.TrackOrder) != len(before.TrackOrder)+1 {
		t.Fatalf("duplicate track: %d → %d tracks", len(before.TrackOrder), len(dup.TrackOrder))
	}
	orig, copyT := dup.Tracks[dup.TrackOrder[0]], dup.Tracks[dup.TrackOrder[1]]
	if orig.Name != "Bass" || copyT.Name != "Bass" || orig.ID == copyT.ID {
		t.Errorf("duplicate did not get its own track id: %s / %s", orig.ID, copyT.ID)
	}
	for id := range copyT.Fx {
		if orig.Fx[id] != nil {
			t.Errorf("duplicate shares fx id %s", id)
		}
	}
	for id := range copyT.Envelopes {
		if orig.Envelopes[id] != nil {
			t.Errorf("duplicate shares envelope id %s", id)
		}
	}

	// Renamed and moved to the end, the original keeps its id.
	last := after.Tracks[after.TrackOrder[len(after.TrackOrder)-1]]
	if last.ID != orig.ID || last.Name != "Sub Bass" {
		t.Errorf("after rename+move: id %s name %q, want id %s", last.ID, last.Name, orig.ID)
	}

	// A track pasted into another project keeps its track and fx ids.
	pasted, _ := realProject(t, filepath.Join("other", "pasted-track.rpp"))
	pt := pasted.Tracks[pasted.TrackOrder[0]]
	if pt.ID != orig.ID {
		t.Errorf("pasted track id %s, source %s", pt.ID, orig.ID)
	}
	for id := range pt.Envelopes {
		if last.Envelopes[id] != nil {
			t.Errorf("pasted envelope kept id %s; the notes in design.md say it does not", id)
		}
	}
}

// resaveInReaper opens a project in a new Reaper instance, has it saved
// under another name, and returns the model of what Reaper wrote.
func resaveInReaper(t *testing.T, bin, file string) *ir.Project {
	t.Helper()
	out := strings.TrimSuffix(file, ".rpp") + "-resaved.rpp"
	script := filepath.Join(testutil.Root(t), "tools", "reaper", "resave.lua")
	cmd := exec.Command(bin, "-newinst", "-nosplash", file, script)
	cmd.Env = append(os.Environ(), "SOUNDGIT_RESAVE_OUT="+out)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(out + ".done"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Reaper did not save %s within 60 seconds", filepath.Base(file))
		}
		time.Sleep(200 * time.Millisecond)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Reaper's save of %s does not parse: %v", filepath.Base(file), err)
	}
	return p
}

func report(a, b *ir.Project) string {
	var sb strings.Builder
	diff.Text(&sb, diff.Projects(a, b, false), "  ")
	return sb.String()
}

// A file written from an edited model must be accepted by Reaper as is.
//
// Reaper itself adjusts a project when it reopens and saves it, so the
// yardstick is Reaper's own round trip of the untouched original: opening
// and saving our edited file must show our edits and nothing else on top
// of that.
//
// Needs Reaper, so it only runs when SOUNDGIT_REAPER names the executable:
//
//	SOUNDGIT_REAPER=/Applications/REAPER.app/Contents/MacOS/REAPER go test ./internal/adapters/reaper -run Survives -v
func TestWrittenFileSurvivesReaper(t *testing.T) {
	bin := os.Getenv("SOUNDGIT_REAPER")
	if bin == "" {
		t.Skip("SOUNDGIT_REAPER is not set")
	}
	original, src := realProject(t, "20-sends.rpp")
	p, _ := realProject(t, "20-sends.rpp")

	// Edits of every kind the writer has to render.
	bass, kick, rev := trackByName(t, p, "Bass"), trackByName(t, p, `Kick "in"`), trackByName(t, p, "Reverb")
	bass.Name = "Bass `sub` \"DI\""
	bass.Volume, bass.Pan, bass.Mute = 0.7, 0.3, true
	bass.FxOrder = []string{bass.FxOrder[1], bass.FxOrder[0]}
	bass.Fx[bass.FxOrder[0]].Bypass = true
	for _, e := range bass.Envelopes {
		if e.Target == "volume" {
			e.Points[1].Value = 0.6
			e.Points = append(e.Points[:2], e.Points[3:]...)
		}
	}
	it := kick.Items[kick.ItemOrder[0]]
	it.Position, it.Length, it.FadeIn, it.ActiveTake = 3, 0.5, 0.02, 0
	it.Takes[0].Name = "renamed take"
	rev.Receives[0].Volume = 0.25
	// Move the receiving track to the front: sources are referenced by position.
	p.TrackOrder = append([]string{rev.ID}, p.TrackOrder[:len(p.TrackOrder)-1]...)
	p.Markers[0].Name, p.Markers[0].Time = "Intro", 1

	dir := t.TempDir()
	if err := os.CopyFS(filepath.Join(dir, "Media"), os.DirFS(filepath.Join(filepath.Dir(src), "Media"))); err != nil {
		t.Fatal(err)
	}
	untouched, written := filepath.Join(dir, "original.rpp"), filepath.Join(dir, "written.rpp")
	data, _ := os.ReadFile(src)
	if err := os.WriteFile(untouched, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(written, Write(p), 0o644); err != nil {
		t.Fatal(err)
	}

	reaperNoise := report(original, resaveInReaper(t, bin, untouched))
	t.Logf("what Reaper changes in its own project on reopen and save:\n%s", reaperNoise)

	got := report(p, resaveInReaper(t, bin, written))
	// Track names differ between the two reports only where we renamed.
	got = strings.ReplaceAll(got, `track "Bass `+"`sub`"+` \"DI\""`, `track "Bass"`)
	lines := func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		sort.Strings(out)
		return out
	}
	want, have := lines(reaperNoise), lines(got)
	if strings.Join(have, "\n") != strings.Join(want, "\n") {
		t.Errorf("Reaper read our file differently from the model, beyond its own adjustments:\n%s", got)
	}
}
