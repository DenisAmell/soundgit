package reaper

import (
	"sort"
	"strconv"
	"strings"

	"github.com/DenisAmell/soundgit/core/internal/ir"
	"github.com/DenisAmell/soundgit/core/internal/rpp"
)

// Write renders the model as a Reaper project file.
//
// Each entity replays its original layout. A line that carries typed fields
// is reused verbatim unless one of those fields now reads differently; then
// only the affected tokens are replaced. Children of one kind (tracks, items,
// effects, takes, markers, receives) are written together where the first of
// them stood, in model order, so reordering in the model reorders the file.
func Write(p *ir.Project) []byte {
	w := &writer{p: p}
	w.project()

	eol := p.Format.EOL
	if eol == "" {
		eol = "\n"
	}
	var b strings.Builder
	if p.Format.BOM {
		b.WriteString("\xEF\xBB\xBF")
	}
	b.WriteString(strings.Join(w.lines, eol))
	if p.Format.FinalNewline {
		b.WriteString(eol)
	}
	return []byte(b.String())
}

type writer struct {
	p     *ir.Project
	lines []string
	depth int
}

func (w *writer) line(s string) {
	if s == "" {
		w.lines = append(w.lines, "")
		return
	}
	w.lines = append(w.lines, strings.Repeat("  ", w.depth)+s)
}

// raw writes stored text that may span lines and contain nested blocks.
func (w *writer) raw(s string) {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimRight(l, " \t") == ">" && w.depth > 0 {
			w.depth--
			w.line(l)
			continue
		}
		w.line(l)
		if strings.HasPrefix(l, "<") {
			w.depth++
		}
	}
}

func (w *writer) open(s, fallback string) {
	if s == "" {
		s = fallback
	}
	w.line(s)
	w.depth++
}

func (w *writer) close() {
	w.depth--
	w.line(">")
}

func (w *writer) project() {
	p := w.p
	w.open(p.Ext.Open, "<REAPER_PROJECT 0.1")
	done := map[string]bool{}
	envs := map[string]bool{}
	for _, e := range p.Ext.Layout {
		switch {
		case e.Key == "@track":
			if !done["track"] {
				done["track"] = true
				w.tracks()
			}
		case e.Key == "@env":
			if env := p.Envelopes[e.Ref]; env != nil {
				envs[e.Ref] = true
				w.envelope(env)
			}
		case e.Known && e.Key == "MARKER":
			if !done["marker"] {
				done["marker"] = true
				w.markers()
			}
		case e.Known && e.Key == "SAMPLERATE":
			w.line(patch(e, number(1, p.Meta.SampleRate)))
		case e.Known && e.Key == "TEMPO":
			w.line(patch(e, number(1, p.Tempo.BPM), number(2, float64(p.Tempo.Num)), number(3, float64(p.Tempo.Den))))
		default:
			w.raw(e.Raw)
		}
	}
	if !done["marker"] {
		w.markers()
	}
	w.leftoverEnvelopes(p.Envelopes, envs)
	if !done["track"] {
		w.tracks()
	}
	w.close()
}

func (w *writer) markers() {
	for _, m := range w.p.Markers {
		w.line(patch(ir.Entry{Key: "MARKER", Raw: m.Raw},
			number(1, float64(m.Index)), number(2, m.Time), str(3, m.Name)))
	}
}

func (w *writer) tracks() {
	for _, id := range w.p.TrackOrder {
		if t := w.p.Tracks[id]; t != nil {
			w.track(t)
		}
	}
}

func (w *writer) leftoverEnvelopes(all map[string]*ir.Envelope, written map[string]bool) {
	var ids []string
	for id := range all {
		if !written[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		w.envelope(all[id])
	}
}

func (w *writer) track(t *ir.Track) {
	w.open(t.Ext.Open, "<TRACK")
	done := map[string]bool{}
	envs := map[string]bool{}
	for _, e := range t.Ext.Layout {
		switch {
		case e.Key == "@fxchain":
			done["fx"] = true
			w.fxChain(t)
		case e.Key == "@item":
			if !done["item"] {
				done["item"] = true
				w.items(t)
			}
		case e.Key == "@env":
			if env := t.Envelopes[e.Ref]; env != nil {
				envs[e.Ref] = true
				w.envelope(env)
			}
		case !e.Known:
			w.raw(e.Raw)
		case e.Key == "NAME":
			w.line(patch(e, str(1, t.Name)))
		case e.Key == "PEAKCOL":
			w.line(patch(e, number(1, float64(t.Color))))
		case e.Key == "VOLPAN":
			w.line(patch(e, number(1, t.Volume), number(2, t.Pan)))
		case e.Key == "MUTESOLO":
			w.line(patch(e, boolean(1, t.Mute), number(2, float64(t.Solo))))
		case e.Key == "IPHASE":
			w.line(patch(e, boolean(1, t.Phase)))
		case e.Key == "ISBUS":
			// The first value is a role derived from the depth change.
			role := 0.0
			if t.FolderDepth > 0 {
				role = 1
			} else if t.FolderDepth < 0 {
				role = 2
			}
			depth := number(2, float64(t.FolderDepth))
			if depth.same(rpp.Tokenize(e.Raw)) {
				w.line(e.Raw)
			} else {
				w.line(patch(e, number(1, role), depth))
			}
		case e.Key == "AUXRECV":
			if !done["recv"] {
				done["recv"] = true
				w.receives(t)
			}
		default:
			w.line(e.Raw)
		}
	}
	if !done["recv"] {
		w.receives(t)
	}
	w.leftoverEnvelopes(t.Envelopes, envs)
	if !done["fx"] && len(t.FxOrder) > 0 {
		w.fxChain(t)
	}
	if !done["item"] {
		w.items(t)
	}
	w.close()
}

func (w *writer) receives(t *ir.Track) {
	for _, r := range t.Receives {
		src := -1
		for i, id := range w.p.TrackOrder {
			if id == r.SrcTrackID {
				src = i
			}
		}
		if src < 0 {
			// The source track is unknown: keep the reference as it was read.
			w.line(r.Raw)
			continue
		}
		w.line(patch(ir.Entry{Key: "AUXRECV", Raw: r.Raw},
			number(1, float64(src)), number(2, float64(r.Mode)), number(3, r.Volume), number(4, r.Pan)))
	}
}

func (w *writer) items(t *ir.Track) {
	for _, id := range t.ItemOrder {
		if it := t.Items[id]; it != nil {
			w.item(it)
		}
	}
}

func (w *writer) fxChain(t *ir.Track) {
	w.open(t.FxChainExt.Open, "<FXCHAIN")
	done := false
	all := func() {
		for _, id := range t.FxOrder {
			if fx := t.Fx[id]; fx != nil {
				w.fx(fx)
			}
		}
	}
	for _, e := range t.FxChainExt.Layout {
		if e.Key == "@fx" {
			if !done {
				done = true
				all()
			}
			continue
		}
		w.raw(e.Raw)
	}
	if !done {
		all()
	}
	w.close()
}

func (w *writer) fx(fx *ir.Fx) {
	envs := map[string]bool{}
	for _, e := range fx.Ext.Layout {
		switch {
		case e.Key == "@plugin":
			w.open(e.Raw, "<"+fx.Format)
			if fx.State != "" {
				w.raw(fx.State)
			}
			w.close()
		case e.Key == "@env":
			if env := fx.Envelopes[e.Ref]; env != nil {
				envs[e.Ref] = true
				w.envelope(env)
			}
		case e.Known && e.Key == "BYPASS":
			w.line(patch(e, boolean(1, fx.Bypass), boolean(2, fx.Offline)))
		case e.Known:
			w.line(e.Raw)
		default:
			w.raw(e.Raw)
		}
	}
	w.leftoverEnvelopes(fx.Envelopes, envs)
}

func (w *writer) envelope(env *ir.Envelope) {
	w.open(env.Ext.Open, "<"+envelopeTag(env.Target))
	done := false
	points := func() {
		for _, pt := range env.Points {
			w.line(patch(ir.Entry{Key: "PT", Raw: pt.Raw},
				number(1, pt.Time), number(2, pt.Value), number(3, float64(pt.Shape))))
		}
	}
	for _, e := range env.Ext.Layout {
		switch {
		case e.Key == "@points":
			done = true
			points()
		case e.Known && e.Key == "ACT":
			w.line(patch(e, boolean(1, env.Active)))
		case e.Known:
			w.line(e.Raw)
		default:
			w.raw(e.Raw)
		}
	}
	if !done {
		points()
	}
	w.close()
}

func (w *writer) item(it *ir.Item) {
	w.open(it.Ext.Open, "<ITEM")
	done := false
	takes := func() {
		for i, tk := range it.Takes {
			w.take(it, i, tk)
		}
	}
	for _, e := range it.Ext.Layout {
		switch {
		case e.Key == "@take":
			if !done {
				done = true
				takes()
			}
		case !e.Known:
			w.raw(e.Raw)
		case e.Key == "POSITION":
			w.line(patch(e, number(1, it.Position)))
		case e.Key == "LENGTH":
			w.line(patch(e, number(1, it.Length)))
		case e.Key == "LOOP":
			w.line(patch(e, boolean(1, it.Loop)))
		case e.Key == "FADEIN":
			w.line(patch(e, number(2, it.FadeIn)))
		case e.Key == "FADEOUT":
			w.line(patch(e, number(2, it.FadeOut)))
		case e.Key == "MUTE":
			w.line(patch(e, boolean(1, it.Mute)))
		default:
			w.line(e.Raw)
		}
	}
	if !done {
		takes()
	}
	w.close()
}

func (w *writer) take(it *ir.Item, idx int, tk *ir.Take) {
	if idx > 0 {
		// Only takes after the first have a TAKE line; it marks the active one.
		open := tk.Ext.Open
		wasSel := false
		for _, t := range rpp.Tokenize(open) {
			wasSel = wasSel || t.Text == "SEL"
		}
		isSel := it.ActiveTake == idx
		switch {
		case open != "" && wasSel == isSel:
			w.line(open)
		case isSel:
			w.line("TAKE SEL")
		default:
			w.line("TAKE")
		}
	}
	for _, e := range tk.Ext.Layout {
		switch {
		case e.Key == "@source":
			if tk.Source != nil {
				w.source(tk.Source)
			}
		case !e.Known:
			w.raw(e.Raw)
		case e.Key == "NAME":
			w.line(patch(e, str(1, tk.Name)))
		case e.Key == "SOFFS":
			w.line(patch(e, number(1, tk.Offset)))
		case e.Key == "PLAYRATE":
			w.line(patch(e, number(1, tk.Playrate), number(3, tk.Pitch)))
		default:
			w.line(e.Raw)
		}
	}
}

func (w *writer) source(s *ir.Source) {
	w.open(s.Ext.Open, "<SOURCE "+s.Type)
	for _, e := range s.Ext.Layout {
		switch {
		case e.Key == "@source":
			if s.Inner != nil {
				w.source(s.Inner)
			}
		case e.Key == "@events":
			if s.Midi != nil {
				for _, ev := range s.Midi.Events {
					w.raw(ev)
				}
			}
		case e.Known && e.Key == "FILE":
			// The stored path is normalized; the file keeps its own spelling
			// (for example backslashes) as long as it names the same asset.
			w.line(patch(e, value{idx: 1, text: s.File, equal: func(old string) bool {
				return ir.NormalizePath(old) == s.File
			}}))
		case e.Known && e.Key == "HASDATA" && s.Midi != nil:
			w.line(patch(e, number(2, float64(s.Midi.PPQ))))
		case e.Known:
			w.line(e.Raw)
		default:
			w.raw(e.Raw)
		}
	}
	w.close()
}

// value is the model's reading of one token of a line: its position, the
// text to write if it changed, and how to tell whether the original token
// already means the same thing.
type value struct {
	idx   int
	text  string
	equal func(old string) bool
}

func (v value) same(toks []rpp.Token) bool {
	old := ""
	if v.idx < len(toks) {
		old = toks[v.idx].Text
	}
	return v.equal(old)
}

func number(idx int, f float64) value {
	return value{idx, strconv.FormatFloat(f, 'f', -1, 64), func(old string) bool { return num(old) == f }}
}

func boolean(idx int, b bool) value {
	t := "0"
	if b {
		t = "1"
	}
	return value{idx, t, func(old string) bool { return flag(old) == b }}
}

func str(idx int, s string) value {
	return value{idx, s, func(old string) bool { return old == s }}
}

// patch returns the entry's original line when every value still reads the
// same, and otherwise the line with just the differing tokens replaced.
func patch(e ir.Entry, vals ...value) string {
	toks := rpp.Tokenize(e.Raw)
	if len(toks) == 0 {
		toks = []rpp.Token{{Text: e.Key}}
	}
	changed := e.Raw == ""
	for _, v := range vals {
		if v.same(toks) {
			continue
		}
		for len(toks) <= v.idx {
			toks = append(toks, rpp.Token{Text: "0"})
		}
		toks[v.idx].Text = v.text
		changed = true
	}
	if !changed {
		return e.Raw
	}
	return rpp.FormatTokens(toks)
}

// envelopeTag is the block tag for an envelope that has no original text.
func envelopeTag(target string) string {
	if rest, ok := strings.CutPrefix(target, "param:"); ok {
		return "PARMENV " + rest
	}
	if rest, ok := strings.CutPrefix(target, "reaper:"); ok {
		return rest
	}
	for tag, t := range envelopeTargets {
		if t == target {
			return tag
		}
	}
	return "VOLENV2"
}
