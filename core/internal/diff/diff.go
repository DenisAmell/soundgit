// Package diff compares two project models and reports what changed in
// terms of the project: tracks, items, effects, automation, tempo.
package diff

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/DenisAmell/soundgit/core/internal/ir"
)

// Kinds of change.
const (
	Added    = "added"
	Removed  = "removed"
	Modified = "modified"
	Moved    = "moved"
)

// Span summarizes an edit inside a sequence that has no per-element
// identity (automation points, MIDI events): how many elements differ and
// over which stretch of time.
type Span struct {
	Count int     `json:"count"`
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	Unit  string  `json:"unit"` // "s" or "ticks"
}

// Change is one difference between two versions.
type Change struct {
	Kind   string `json:"kind"`
	Type   string `json:"type"`   // project, track, item, take, fx, envelope, marker, receive
	Entity string `json:"entity"` // readable path, e.g. `track "Bass" / fx "ReaEQ"`
	ID     string `json:"id,omitempty"`
	Field  string `json:"field,omitempty"`
	Old    any    `json:"old,omitempty"`
	New    any    `json:"new,omitempty"`
	Span   *Span  `json:"span,omitempty"`
	Detail string `json:"detail,omitempty"`
	View   bool   `json:"view,omitempty"` // editor state only: cursor, zoom, selection
}

// Projects lists the changes that turn a into b. Editor view state is left
// out unless withView is set.
func Projects(a, b *ir.Project, withView bool) []Change {
	d := &differ{a: a, b: b, view: withView}
	d.project()
	return d.out
}

type differ struct {
	a, b *ir.Project
	view bool
	out  []Change
}

func (d *differ) add(c Change) { d.out = append(d.out, c) }

func (d *differ) field(typ, entity, id, name string, old, new any) {
	if old != new {
		d.add(Change{Kind: Modified, Type: typ, Entity: entity, ID: id, Field: name, Old: old, New: new})
	}
}

func (d *differ) project() {
	a, b := d.a, d.b
	const who = "project"
	d.field("project", who, "", "sampleRate", a.Meta.SampleRate, b.Meta.SampleRate)
	d.field("project", who, "", "tempo", a.Tempo.BPM, b.Tempo.BPM)
	d.field("project", who, "", "timeSignature",
		fmt.Sprintf("%d/%d", a.Tempo.Num, a.Tempo.Den), fmt.Sprintf("%d/%d", b.Tempo.Num, b.Tempo.Den))
	d.markers()
	d.envelopes(who, a.Envelopes, b.Envelopes)
	d.ext("project", who, "", a.Ext, b.Ext)

	for _, id := range a.TrackOrder {
		if b.Tracks[id] == nil {
			d.add(Change{Kind: Removed, Type: "track", Entity: trackName(a.Tracks[id]), ID: id})
		}
	}
	for _, id := range b.TrackOrder {
		if a.Tracks[id] == nil {
			d.add(Change{Kind: Added, Type: "track", Entity: trackName(b.Tracks[id]), ID: id})
		}
	}
	for _, m := range moves(a.TrackOrder, b.TrackOrder) {
		d.add(Change{Kind: Moved, Type: "track", Entity: trackName(b.Tracks[m.id]), ID: m.id,
			Field: "position", Old: m.from + 1, New: m.to + 1})
	}
	for _, id := range b.TrackOrder {
		if at := a.Tracks[id]; at != nil {
			d.track(at, b.Tracks[id])
		}
	}
}

func trackName(t *ir.Track) string {
	if t == nil {
		return "track ?"
	}
	return fmt.Sprintf("track %q", t.Name)
}

// markers compares markers and regions. A region is stored as two entries
// with the same index: its start, which carries the name, and its end.
func (d *differ) markers() {
	type entry struct {
		m   ir.Marker
		end bool
	}
	key := func(ms []ir.Marker) map[string]entry {
		out, seen := map[string]entry{}, map[int]int{}
		for _, m := range ms {
			out[fmt.Sprintf("%d#%d", m.Index, seen[m.Index])] = entry{m, m.Region && seen[m.Index] > 0}
			seen[m.Index]++
		}
		return out
	}
	am, bm := key(d.a.Markers), key(d.b.Markers)
	name := func(ms map[string]entry, e entry) string {
		if !e.m.Region {
			return fmt.Sprintf("marker %d %q", e.m.Index, e.m.Name)
		}
		// The end entry has no name of its own: use the region's.
		start := ms[fmt.Sprintf("%d#0", e.m.Index)]
		return fmt.Sprintf("region %d %q", e.m.Index, start.m.Name)
	}
	for _, k := range sortedKeys(am) {
		if _, ok := bm[k]; !ok && !am[k].end {
			d.add(Change{Kind: Removed, Type: "marker", Entity: name(am, am[k])})
		}
	}
	for _, k := range sortedKeys(bm) {
		y := bm[k]
		x, ok := am[k]
		n := name(bm, y)
		switch {
		case !ok && y.end:
			// Reported together with the region's start.
		case !ok:
			d.add(Change{Kind: Added, Type: "marker", Entity: n})
		case y.end:
			d.field("marker", n, "", "end", x.m.Time, y.m.Time)
		default:
			what := "time"
			if y.m.Region {
				what = "start"
			}
			d.field("marker", n, "", what, x.m.Time, y.m.Time)
			d.field("marker", n, "", "name", x.m.Name, y.m.Name)
		}
	}
}

func (d *differ) track(a, b *ir.Track) {
	who := trackName(b)
	d.field("track", who, b.ID, "name", a.Name, b.Name)
	d.field("track", who, b.ID, "color", a.Color, b.Color)
	if a.Volume != b.Volume {
		d.add(Change{Kind: Modified, Type: "track", Entity: who, ID: b.ID, Field: "volume",
			Old: a.Volume, New: b.Volume, Detail: db(a.Volume) + " → " + db(b.Volume)})
	}
	d.field("track", who, b.ID, "pan", a.Pan, b.Pan)
	d.field("track", who, b.ID, "mute", a.Mute, b.Mute)
	d.field("track", who, b.ID, "solo", a.Solo, b.Solo)
	d.field("track", who, b.ID, "phase", a.Phase, b.Phase)
	d.field("track", who, b.ID, "folderDepth", a.FolderDepth, b.FolderDepth)
	d.receives(who, a, b)
	d.fx(who, a, b)
	d.envelopes(who, a.Envelopes, b.Envelopes)
	d.items(who, a, b)
	d.ext("track", who, b.ID, a.Ext, b.Ext)
	d.ext("track", who+" / fx chain", b.ID, a.FxChainExt, b.FxChainExt)
}

func (d *differ) receives(who string, a, b *ir.Track) {
	key := func(p *ir.Project, rs []ir.Receive) (map[string]ir.Receive, map[string]string) {
		out, names, seen := map[string]ir.Receive{}, map[string]string{}, map[string]int{}
		for _, r := range rs {
			k := fmt.Sprintf("%s#%d", r.SrcTrackID, seen[r.SrcTrackID])
			seen[r.SrcTrackID]++
			out[k] = r
			names[k] = who + " / receive from " + trackName(p.Tracks[r.SrcTrackID])
		}
		return out, names
	}
	ar, an := key(d.a, a.Receives)
	br, bn := key(d.b, b.Receives)
	for _, k := range sortedKeys(ar) {
		if _, ok := br[k]; !ok {
			d.add(Change{Kind: Removed, Type: "receive", Entity: an[k]})
		}
	}
	for _, k := range sortedKeys(br) {
		y := br[k]
		x, ok := ar[k]
		if !ok {
			d.add(Change{Kind: Added, Type: "receive", Entity: bn[k]})
			continue
		}
		d.field("receive", bn[k], "", "volume", x.Volume, y.Volume)
		d.field("receive", bn[k], "", "pan", x.Pan, y.Pan)
		d.field("receive", bn[k], "", "mode", x.Mode, y.Mode)
	}
}

func (d *differ) fx(who string, a, b *ir.Track) {
	name := func(f *ir.Fx) string { return fmt.Sprintf("%s / fx %q", who, f.Name) }
	for _, id := range a.FxOrder {
		if b.Fx[id] == nil {
			d.add(Change{Kind: Removed, Type: "fx", Entity: name(a.Fx[id]), ID: id})
		}
	}
	for _, id := range b.FxOrder {
		if a.Fx[id] == nil {
			d.add(Change{Kind: Added, Type: "fx", Entity: name(b.Fx[id]), ID: id})
		}
	}
	for _, m := range moves(a.FxOrder, b.FxOrder) {
		d.add(Change{Kind: Moved, Type: "fx", Entity: name(b.Fx[m.id]), ID: m.id,
			Field: "position", Old: m.from + 1, New: m.to + 1})
	}
	for _, id := range b.FxOrder {
		x, y := a.Fx[id], b.Fx[id]
		if x == nil {
			continue
		}
		n := name(y)
		d.field("fx", n, id, "plugin", x.Format+" "+x.Ident, y.Format+" "+y.Ident)
		d.field("fx", n, id, "name", x.Name, y.Name)
		d.field("fx", n, id, "bypass", x.Bypass, y.Bypass)
		d.field("fx", n, id, "offline", x.Offline, y.Offline)
		if x.StateHash != y.StateHash {
			d.add(Change{Kind: Modified, Type: "fx", Entity: n, ID: id, Field: "state",
				Detail: "plugin settings changed"})
		}
		d.envelopes(n, x.Envelopes, y.Envelopes)
		d.ext("fx", n, id, x.Ext, y.Ext)
	}
}

func envelopeName(owner string, e *ir.Envelope) string {
	return owner + " / " + e.Target + " automation"
}

func (d *differ) envelopes(owner string, a, b map[string]*ir.Envelope) {
	for _, id := range sortedKeys(a) {
		if b[id] == nil {
			d.add(Change{Kind: Removed, Type: "envelope", Entity: envelopeName(owner, a[id]), ID: id})
		}
	}
	for _, id := range sortedKeys(b) {
		y := b[id]
		x := a[id]
		n := envelopeName(owner, y)
		if x == nil {
			d.add(Change{Kind: Added, Type: "envelope", Entity: n, ID: id})
			continue
		}
		d.field("envelope", n, id, "active", x.Active, y.Active)
		pk := func(ps []ir.Point) []timed {
			out := make([]timed, len(ps))
			for i, p := range ps {
				out[i] = timed{p.Time, fmt.Sprintf("%v|%v|%d", p.Time, p.Value, p.Shape)}
			}
			return out
		}
		if s := span(pk(x.Points), pk(y.Points), "s"); s != nil {
			d.add(Change{Kind: Modified, Type: "envelope", Entity: n, ID: id, Field: "points", Span: s})
		}
		d.ext("envelope", n, id, x.Ext, y.Ext)
	}
}

func (d *differ) items(who string, a, b *ir.Track) {
	name := func(it *ir.Item) string {
		label := ""
		if len(it.Takes) > 0 {
			if n := it.Takes[min(it.ActiveTake, len(it.Takes)-1)].Name; n != "" {
				label = fmt.Sprintf(" %q", n)
			}
		}
		return fmt.Sprintf("%s / item%s at %ss", who, label, num(it.Position))
	}
	for _, id := range a.ItemOrder {
		if b.Items[id] == nil {
			d.add(Change{Kind: Removed, Type: "item", Entity: name(a.Items[id]), ID: id})
		}
	}
	for _, id := range b.ItemOrder {
		x, y := a.Items[id], b.Items[id]
		n := name(y)
		if x == nil {
			d.add(Change{Kind: Added, Type: "item", Entity: n, ID: id})
			continue
		}
		d.field("item", n, id, "position", x.Position, y.Position)
		d.field("item", n, id, "length", x.Length, y.Length)
		d.field("item", n, id, "loop", x.Loop, y.Loop)
		d.field("item", n, id, "fadeIn", x.FadeIn, y.FadeIn)
		d.field("item", n, id, "fadeOut", x.FadeOut, y.FadeOut)
		d.field("item", n, id, "mute", x.Mute, y.Mute)
		d.field("item", n, id, "activeTake", x.ActiveTake+1, y.ActiveTake+1)
		d.takes(n, x, y)
		d.ext("item", n, id, x.Ext, y.Ext)
	}
}

func (d *differ) takes(item string, a, b *ir.Item) {
	index := func(it *ir.Item) map[string]*ir.Take {
		out := map[string]*ir.Take{}
		for _, t := range it.Takes {
			out[t.ID] = t
		}
		return out
	}
	at, bt := index(a), index(b)
	name := func(t *ir.Take) string {
		if t.Name == "" {
			return item + " / take"
		}
		return fmt.Sprintf("%s / take %q", item, t.Name)
	}
	for _, t := range a.Takes {
		if bt[t.ID] == nil {
			d.add(Change{Kind: Removed, Type: "take", Entity: name(t), ID: t.ID})
		}
	}
	for _, y := range b.Takes {
		x := at[y.ID]
		n := name(y)
		if x == nil {
			d.add(Change{Kind: Added, Type: "take", Entity: n, ID: y.ID})
			continue
		}
		d.field("take", n, y.ID, "name", x.Name, y.Name)
		d.field("take", n, y.ID, "offset", x.Offset, y.Offset)
		d.field("take", n, y.ID, "playrate", x.Playrate, y.Playrate)
		d.field("take", n, y.ID, "pitch", x.Pitch, y.Pitch)
		d.source(n, y.ID, x.Source, y.Source)
		d.ext("take", n, y.ID, x.Ext, y.Ext)
	}
}

func (d *differ) source(take, id string, a, b *ir.Source) {
	for a != nil || b != nil {
		if a == nil || b == nil {
			d.add(Change{Kind: Modified, Type: "take", Entity: take, ID: id, Field: "source",
				Old: sourceLabel(a), New: sourceLabel(b)})
			return
		}
		d.field("take", take, id, "sourceType", a.Type, b.Type)
		d.field("take", take, id, "file", a.File, b.File)
		if a.Midi != nil && b.Midi != nil {
			d.field("take", take, id, "midiResolution", a.Midi.PPQ, b.Midi.PPQ)
			if s := span(midiEvents(a.Midi), midiEvents(b.Midi), "ticks"); s != nil {
				d.add(Change{Kind: Modified, Type: "take", Entity: take, ID: id, Field: "midi", Span: s})
			}
		}
		d.ext("take", take+" / source", id, a.Ext, b.Ext)
		a, b = a.Inner, b.Inner
	}
}

func sourceLabel(s *ir.Source) any {
	if s == nil {
		return nil
	}
	return strings.TrimSpace(s.Type + " " + s.File)
}

// midiEvents places each event at its absolute tick: events are stored with
// the delta from the previous one as their second token.
func midiEvents(m *ir.Midi) []timed {
	out := make([]timed, 0, len(m.Events))
	tick := 0.0
	for _, ev := range m.Events {
		head, rest, _ := strings.Cut(ev, "\n")
		f := strings.Fields(head)
		payload := rest
		if len(f) >= 2 {
			delta, _ := strconv.ParseFloat(f[1], 64)
			tick += delta
			payload = strings.ToLower(strings.TrimPrefix(f[0], "<")) + " " + strings.Join(f[2:], " ") + "\n" + rest
		}
		out = append(out, timed{tick, fmt.Sprintf("%v|%s", tick, payload)})
	}
	return out
}

type timed struct {
	at  float64
	key string
}

// span compares two sequences as multisets of positioned elements. An
// element that changed counts once, although it disappears from one side
// and appears on the other.
func span(a, b []timed, unit string) *Span {
	count := map[string]int{}
	for _, x := range a {
		count[x.key]++
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	touch := func(t float64) { lo, hi = math.Min(lo, t), math.Max(hi, t) }
	added := 0
	for _, y := range b {
		if count[y.key] > 0 {
			count[y.key]--
			continue
		}
		added++
		touch(y.at)
	}
	removed := 0
	for _, x := range a {
		if count[x.key] > 0 {
			count[x.key]--
			removed++
			touch(x.at)
		}
	}
	if added == 0 && removed == 0 {
		return nil
	}
	return &Span{Count: max(added, removed), From: lo, To: hi, Unit: unit}
}

// ext reports changes in data the adapter carries through without
// understanding it, keyed by attribute name and occurrence.
func (d *differ) ext(typ, entity, id string, a, b ir.Ext) {
	index := func(e ir.Ext) (map[string]ir.Entry, []string) {
		out, seen := map[string]ir.Entry{}, map[string]int{}
		var order []string
		for _, en := range e.Layout {
			if en.Known || strings.HasPrefix(en.Key, "@") {
				continue
			}
			k := en.Key
			if n := seen[en.Key]; n > 0 {
				k = fmt.Sprintf("%s#%d", en.Key, n+1)
			}
			seen[en.Key]++
			out[k] = en
			order = append(order, k)
		}
		return out, order
	}
	am, ao := index(a)
	bm, bo := index(b)
	report := func(kind, k string, old, new ir.Entry) {
		view := old.View || new.View
		if view && !d.view {
			return
		}
		c := Change{Kind: Modified, Type: typ, Entity: entity, ID: id, Field: "other:" + k, View: view}
		switch kind {
		case Added:
			c.Detail = "unrecognized data added"
		case Removed:
			c.Detail = "unrecognized data removed"
		default:
			c.Detail = "unrecognized data changed"
		}
		// Short single-line values are worth showing; blobs are not.
		if short(old.Raw) && short(new.Raw) {
			if old.Raw != "" {
				c.Old = old.Raw
			}
			if new.Raw != "" {
				c.New = new.Raw
			}
		}
		d.add(c)
	}
	for _, k := range ao {
		if _, ok := bm[k]; !ok {
			report(Removed, k, am[k], ir.Entry{})
		}
	}
	for _, k := range bo {
		y := bm[k]
		x, ok := am[k]
		switch {
		case !ok:
			report(Added, k, ir.Entry{}, y)
		case x.Raw != y.Raw:
			report(Modified, k, x, y)
		}
	}
}

func short(s string) bool { return len(s) <= 100 && !strings.Contains(s, "\n") }

type move struct {
	id       string
	from, to int
}

// moves finds the elements whose relative order changed: everything outside
// a longest common subsequence of the two orders, among ids present in both.
func moves(a, b []string) []move {
	inB := map[string]int{}
	for i, id := range b {
		inB[id] = i
	}
	inA := map[string]int{}
	var ca []string
	for i, id := range a {
		inA[id] = i
		if _, ok := inB[id]; ok {
			ca = append(ca, id)
		}
	}
	var cb []string
	for _, id := range b {
		if _, ok := inA[id]; ok {
			cb = append(cb, id)
		}
	}
	n := len(ca)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, n+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if ca[i] == cb[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	keep := map[string]bool{}
	for i, j := 0, 0; i < n && j < n; {
		switch {
		case ca[i] == cb[j]:
			keep[ca[i]] = true
			i, j = i+1, j+1
		case lcs[i+1][j] >= lcs[i][j+1]:
			i++
		default:
			j++
		}
	}
	var out []move
	for _, id := range cb {
		if !keep[id] {
			out = append(out, move{id, inA[id], inB[id]})
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// db renders a linear gain the way a mixer shows it.
func db(gain float64) string {
	if gain <= 0 {
		return "-inf dB"
	}
	return fmt.Sprintf("%+.1f dB", 20*math.Log10(gain))
}
