// Package reaper converts between Reaper project files and the IR.
//
// Reading goes file → rpp tree → IR. Every line the adapter understands
// fills typed fields and is remembered verbatim; every other line or block
// is stored as an opaque entry of the entity that owns it. Writing replays
// each entity's layout, re-rendering only the tokens whose typed value
// changed, so an untouched project comes back identical.
package reaper

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/DenisAmell/soundgit/core/internal/hashing"
	"github.com/DenisAmell/soundgit/core/internal/ir"
	"github.com/DenisAmell/soundgit/core/internal/rpp"
)

// Parse reads a Reaper project file into the model.
func Parse(data []byte) (*ir.Project, error) {
	f, err := rpp.Parse(data)
	if err != nil {
		return nil, err
	}
	return FromTree(f)
}

type parser struct {
	seen     map[string]string // "kind\x00id" → description of the first owner
	trackIDs []string
}

// FromTree converts a parsed tree into the model.
func FromTree(f *rpp.File) (*ir.Project, error) {
	root := f.Root
	ps := &parser{seen: map[string]string{}}
	p := &ir.Project{
		IRVersion: ir.Version,
		Meta:      ir.Meta{Daw: "reaper", DawVersion: root.Arg(1)},
		Format:    ir.Format{EOL: f.EOL, FinalNewline: root.CloseEOL != "", BOM: f.BOM},
		Tempo:     ir.Tempo{BPM: 120, Num: 4, Den: 4},
		Envelopes: map[string]*ir.Envelope{},
		Tracks:    map[string]*ir.Track{},
		Ext:       ir.Ext{Open: text(root)},
	}

	// Track ids are needed up front: receives refer to tracks by position.
	var trackNodes []*rpp.Node
	for _, c := range root.Children {
		if c.Block && c.Tag() == "TRACK" {
			trackNodes = append(trackNodes, c)
		}
	}
	for i, n := range trackNodes {
		id := ""
		for _, c := range n.Children {
			if !c.Block && c.Tag() == "TRACKID" {
				id = c.Arg(0)
			}
		}
		if id == "" {
			id = n.Arg(0)
		}
		if id == "" {
			id = fmt.Sprintf("track:%d", i)
		}
		ps.trackIDs = append(ps.trackIDs, id)
	}

	envSeen := map[string]int{}
	ti := 0
	for _, c := range root.Children {
		tag := c.Tag()
		switch {
		case !c.Block && tag == "SAMPLERATE":
			p.Meta.SampleRate = num(c.Arg(0))
			p.Ext.Layout = append(p.Ext.Layout, known(c))
		case !c.Block && tag == "TEMPO":
			p.Tempo = ir.Tempo{BPM: num(c.Arg(0)), Num: integer(c.Arg(1)), Den: integer(c.Arg(2))}
			p.Ext.Layout = append(p.Ext.Layout, known(c))
		case !c.Block && tag == "MARKER":
			p.Markers = append(p.Markers, ir.Marker{
				Index:  integer(c.Arg(0)),
				Time:   num(c.Arg(1)),
				Name:   c.Arg(2),
				Region: integer(c.Arg(3))&1 == 1,
				Raw:    text(c),
			})
			p.Ext.Layout = append(p.Ext.Layout, ir.Entry{Key: "MARKER", Known: true})
		case c.Block && tag == "TRACK":
			id := ps.trackIDs[ti]
			ti++
			if err := ps.claim("track", id, fmt.Sprintf("track #%d (line %d)", ti, c.Line)); err != nil {
				return nil, err
			}
			t, err := ps.track(c, id)
			if err != nil {
				return nil, err
			}
			p.Tracks[id] = t
			p.TrackOrder = append(p.TrackOrder, id)
			p.Ext.Layout = append(p.Ext.Layout, ir.Entry{Key: "@track", Ref: id})
		case c.Block && isEnvelope(tag):
			e, err := ps.envelope(c, "project", envSeen)
			if err != nil {
				return nil, err
			}
			p.Envelopes[e.ID] = e
			p.Ext.Layout = append(p.Ext.Layout, ir.Entry{Key: "@env", Ref: e.ID})
		default:
			p.Ext.Layout = append(p.Ext.Layout, opaque(c))
		}
	}
	return p, nil
}

func (ps *parser) claim(kind, id, desc string) error {
	key := kind + "\x00" + id
	if first, dup := ps.seen[key]; dup {
		return fmt.Errorf("duplicate %s id %s: used by %s and by %s", kind, id, first, desc)
	}
	ps.seen[key] = desc
	return nil
}

func (ps *parser) track(n *rpp.Node, id string) (*ir.Track, error) {
	t := &ir.Track{
		ID:        id,
		Volume:    1,
		Fx:        map[string]*ir.Fx{},
		Envelopes: map[string]*ir.Envelope{},
		Items:     map[string]*ir.Item{},
		Ext:       ir.Ext{Open: text(n)},
	}
	envSeen := map[string]int{}
	add := func(e ir.Entry) { t.Ext.Layout = append(t.Ext.Layout, e) }
	for _, c := range n.Children {
		tag := c.Tag()
		switch {
		case c.Block && tag == "FXCHAIN":
			if err := ps.fxChain(c, t); err != nil {
				return nil, err
			}
			add(ir.Entry{Key: "@fxchain"})
		case c.Block && tag == "ITEM":
			it, err := ps.item(c, t)
			if err != nil {
				return nil, err
			}
			t.Items[it.ID] = it
			t.ItemOrder = append(t.ItemOrder, it.ID)
			add(ir.Entry{Key: "@item", Ref: it.ID})
		case c.Block && isEnvelope(tag):
			e, err := ps.envelope(c, t.ID, envSeen)
			if err != nil {
				return nil, err
			}
			t.Envelopes[e.ID] = e
			add(ir.Entry{Key: "@env", Ref: e.ID})
		case c.Block:
			add(opaque(c))
		case tag == "NAME":
			t.Name = c.Arg(0)
			add(known(c))
		case tag == "PEAKCOL":
			t.Color = integer(c.Arg(0))
			add(known(c))
		case tag == "VOLPAN":
			t.Volume, t.Pan = num(c.Arg(0)), num(c.Arg(1))
			add(known(c))
		case tag == "MUTESOLO":
			t.Mute, t.Solo = flag(c.Arg(0)), integer(c.Arg(1))
			add(known(c))
		case tag == "IPHASE":
			t.Phase = flag(c.Arg(0))
			add(known(c))
		case tag == "ISBUS":
			t.FolderDepth = integer(c.Arg(1))
			add(known(c))
		case tag == "TRACKID":
			add(known(c))
		case tag == "AUXRECV":
			r := ir.Receive{
				Mode:   integer(c.Arg(1)),
				Volume: num(c.Arg(2)),
				Pan:    num(c.Arg(3)),
				Raw:    text(c),
			}
			if i, err := strconv.Atoi(c.Arg(0)); err == nil && i >= 0 && i < len(ps.trackIDs) {
				r.SrcTrackID = ps.trackIDs[i]
			}
			t.Receives = append(t.Receives, r)
			add(ir.Entry{Key: "AUXRECV", Known: true})
		default:
			add(opaque(c))
		}
	}
	return t, nil
}

func (ps *parser) fxChain(n *rpp.Node, t *ir.Track) error {
	t.FxChainExt.Open = text(n)
	// The chain is flat: a BYPASS line starts each effect's group of lines.
	var groups [][]*rpp.Node
	for _, c := range n.Children {
		if !c.Block && c.Tag() == "BYPASS" {
			groups = append(groups, []*rpp.Node{c})
			continue
		}
		if len(groups) == 0 {
			t.FxChainExt.Layout = append(t.FxChainExt.Layout, opaque(c))
			continue
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], c)
	}
	for i, g := range groups {
		fx := &ir.Fx{Envelopes: map[string]*ir.Envelope{}}
		for _, c := range g {
			if !c.Block && c.Tag() == "FXID" {
				fx.ID = c.Arg(0)
			}
		}
		if fx.ID == "" {
			fx.ID = fmt.Sprintf("fx:%s:%d", t.ID, i)
		}
		if err := ps.claim("fx", fx.ID, fmt.Sprintf("effect #%d of track %q (line %d)", i+1, t.Name, g[0].Line)); err != nil {
			return err
		}
		envSeen := map[string]int{}
		plugin := false
		for _, c := range g {
			tag := c.Tag()
			switch {
			case !c.Block && tag == "BYPASS":
				fx.Bypass, fx.Offline = flag(c.Arg(0)), flag(c.Arg(1))
				fx.Ext.Layout = append(fx.Ext.Layout, known(c))
			case !c.Block && tag == "FXID":
				fx.Ext.Layout = append(fx.Ext.Layout, known(c))
			case c.Block && isEnvelope(tag):
				e, err := ps.envelope(c, fx.ID, envSeen)
				if err != nil {
					return err
				}
				fx.Envelopes[e.ID] = e
				fx.Ext.Layout = append(fx.Ext.Layout, ir.Entry{Key: "@env", Ref: e.ID})
			case c.Block && !plugin && tag != "COMMENT":
				plugin = true
				fx.Format, fx.Name, fx.Ident = pluginIdentity(c)
				fx.State = body(c)
				fx.StateHash = hashing.Object("blob", []byte(fx.State))
				fx.Ext.Layout = append(fx.Ext.Layout, ir.Entry{Key: "@plugin", Raw: text(c)})
			default:
				fx.Ext.Layout = append(fx.Ext.Layout, opaque(c))
			}
		}
		t.Fx[fx.ID] = fx
		t.FxOrder = append(t.FxOrder, fx.ID)
		t.FxChainExt.Layout = append(t.FxChainExt.Layout, ir.Entry{Key: "@fx", Ref: fx.ID})
	}
	return nil
}

func pluginIdentity(n *rpp.Node) (format, name, ident string) {
	tag := n.Tag()
	name = n.Arg(0)
	format = tag
	switch tag {
	case "VST":
		// The display name starts with the real flavour: "VST3: ...", "VSTi: ...".
		if i := strings.Index(name, ":"); i > 0 && strings.HasPrefix(name, "VST") {
			format = name[:i]
		}
		if ident = n.Arg(4); ident == "" {
			ident = n.Arg(1)
		}
	case "AU":
		if n.Arg(5) != "" {
			ident = n.Arg(3) + "/" + n.Arg(4) + "/" + n.Arg(5)
		} else {
			ident = n.Arg(1)
		}
	case "JS":
		ident = name
	default:
		if ident = n.Arg(1); ident == "" {
			ident = name
		}
	}
	return
}

func (ps *parser) envelope(n *rpp.Node, owner string, seen map[string]int) (*ir.Envelope, error) {
	e := &ir.Envelope{Target: envelopeTarget(n), Ext: ir.Ext{Open: text(n)}}
	points := false
	for _, c := range n.Children {
		tag := c.Tag()
		switch {
		case c.Block:
			e.Ext.Layout = append(e.Ext.Layout, opaque(c))
		case tag == "EGUID":
			e.ID = c.Arg(0)
			e.Ext.Layout = append(e.Ext.Layout, known(c))
		case tag == "ACT":
			e.Active = flag(c.Arg(0))
			e.Ext.Layout = append(e.Ext.Layout, known(c))
		case tag == "PT":
			e.Points = append(e.Points, ir.Point{
				Time:  num(c.Arg(0)),
				Value: num(c.Arg(1)),
				Shape: integer(c.Arg(2)),
				Raw:   text(c),
			})
			if !points {
				points = true
				e.Ext.Layout = append(e.Ext.Layout, ir.Entry{Key: "@points"})
			}
		default:
			e.Ext.Layout = append(e.Ext.Layout, opaque(c))
		}
	}
	if e.ID == "" {
		// No id in the file: derive a stable one from the owner and the role.
		e.ID = "env:" + owner + ":" + e.Target
		if k := seen[e.Target]; k > 0 {
			e.ID += fmt.Sprintf("#%d", k)
		}
	}
	seen[e.Target]++
	if err := ps.claim("envelope", e.ID, fmt.Sprintf("envelope %s (line %d)", e.Target, n.Line)); err != nil {
		return nil, err
	}
	return e, nil
}

// takeOwned lists attributes that describe a take rather than the item.
var takeOwned = map[string]bool{
	"NAME": true, "VOLPAN": true, "SOFFS": true, "PLAYRATE": true, "CHANMODE": true,
	"GUID": true, "RECPASS": true, "TAKECOLOR": true, "TAKEVOLPAN": true, "TMINFO": true,
	"TAKEMARKER": true, "SM": true, "SOURCE": true, "TAKEFX": true,
	"TAKE_FX_HAVE_NEW_PDC_AUTOMATION_BEHAVIOR": true,
}

func (ps *parser) item(n *rpp.Node, t *ir.Track) (*ir.Item, error) {
	it := &ir.Item{Ext: ir.Ext{Open: text(n)}}
	for _, c := range n.Children {
		if !c.Block && c.Tag() == "IGUID" {
			it.ID = c.Arg(0)
		}
	}
	if it.ID == "" {
		it.ID = fmt.Sprintf("item:%s:%d", t.ID, len(t.ItemOrder))
	}
	if err := ps.claim("item", it.ID, fmt.Sprintf("item on track %q (line %d)", t.Name, n.Line)); err != nil {
		return nil, err
	}

	// The first take's attributes sit directly in the item; each further
	// take starts at a TAKE line.
	var takes [][]*rpp.Node
	var opens []string
	add := func(e ir.Entry) { it.Ext.Layout = append(it.Ext.Layout, e) }
	for _, c := range n.Children {
		tag := c.Tag()
		if !c.Block && tag == "TAKE" {
			if len(takes) == 0 {
				takes = append(takes, nil) // an item whose first take is empty
				opens = append(opens, "")
				add(ir.Entry{Key: "@take", Ref: "0"})
			}
			for _, a := range c.Tokens[1:] {
				if a.Text == "SEL" {
					it.ActiveTake = len(takes)
				}
			}
			add(ir.Entry{Key: "@take", Ref: strconv.Itoa(len(takes))})
			takes = append(takes, nil)
			opens = append(opens, text(c))
			continue
		}
		if len(takes) > 1 || takeOwned[tag] || (c.Block && isEnvelope(tag)) {
			if len(takes) == 0 {
				takes = append(takes, nil)
				opens = append(opens, "")
				add(ir.Entry{Key: "@take", Ref: "0"})
			}
			takes[len(takes)-1] = append(takes[len(takes)-1], c)
			continue
		}
		switch tag {
		case "POSITION":
			it.Position = num(c.Arg(0))
			add(known(c))
		case "LENGTH":
			it.Length = num(c.Arg(0))
			add(known(c))
		case "LOOP":
			it.Loop = flag(c.Arg(0))
			add(known(c))
		case "FADEIN":
			it.FadeIn = num(c.Arg(1))
			add(known(c))
		case "FADEOUT":
			it.FadeOut = num(c.Arg(1))
			add(known(c))
		case "MUTE":
			it.Mute = flag(c.Arg(0))
			add(known(c))
		case "IGUID":
			add(known(c))
		default:
			add(opaque(c))
		}
	}
	for i, nodes := range takes {
		tk, err := ps.take(nodes, it, i)
		if err != nil {
			return nil, err
		}
		tk.Ext.Open = opens[i]
		it.Takes = append(it.Takes, tk)
	}
	return it, nil
}

func (ps *parser) take(nodes []*rpp.Node, it *ir.Item, idx int) (*ir.Take, error) {
	tk := &ir.Take{Playrate: 1}
	add := func(e ir.Entry) { tk.Ext.Layout = append(tk.Ext.Layout, e) }
	firstLine := 0
	for _, c := range nodes {
		if firstLine == 0 {
			firstLine = c.Line
		}
		tag := c.Tag()
		switch {
		case c.Block && tag == "SOURCE" && tk.Source == nil:
			tk.Source = ps.source(c)
			add(ir.Entry{Key: "@source"})
		case c.Block:
			add(opaque(c))
		case tag == "NAME":
			tk.Name = c.Arg(0)
			add(known(c))
		case tag == "SOFFS":
			tk.Offset = num(c.Arg(0))
			add(known(c))
		case tag == "PLAYRATE":
			tk.Playrate, tk.Pitch = num(c.Arg(0)), num(c.Arg(2))
			add(known(c))
		case tag == "GUID":
			tk.ID = c.Arg(0)
			add(known(c))
		default:
			add(opaque(c))
		}
	}
	if tk.ID == "" {
		tk.ID = fmt.Sprintf("take:%s:%d", it.ID, idx)
	}
	if err := ps.claim("take", tk.ID, fmt.Sprintf("take #%d of item %s (line %d)", idx+1, it.ID, firstLine)); err != nil {
		return nil, err
	}
	return tk, nil
}

var midiEvent = map[string]bool{"E": true, "e": true, "Em": true, "em": true, "X": true, "x": true}

func (ps *parser) source(n *rpp.Node) *ir.Source {
	s := &ir.Source{Type: n.Arg(0), Ext: ir.Ext{Open: text(n)}}
	isMidi := strings.HasPrefix(s.Type, "MIDI")
	if isMidi {
		s.Midi = &ir.Midi{}
	}
	events := false
	add := func(e ir.Entry) { s.Ext.Layout = append(s.Ext.Layout, e) }
	for _, c := range n.Children {
		tag := c.Tag()
		switch {
		case c.Block && tag == "SOURCE" && s.Inner == nil:
			s.Inner = ps.source(c)
			add(ir.Entry{Key: "@source"})
		case isMidi && midiEvent[tag]:
			ev := text(c)
			if c.Block {
				ev = block(c)
			}
			s.Midi.Events = append(s.Midi.Events, ev)
			if !events {
				events = true
				add(ir.Entry{Key: "@events"})
			}
		case c.Block:
			add(opaque(c))
		case tag == "FILE":
			s.File = ir.NormalizePath(c.Arg(0))
			add(known(c))
		case isMidi && tag == "HASDATA":
			s.Midi.PPQ = integer(c.Arg(1))
			add(known(c))
		default:
			add(opaque(c))
		}
	}
	return s
}

// envelopeTargets names what Reaper's envelope blocks automate.
var envelopeTargets = map[string]string{
	"VOLENV2": "volume", "VOLENV": "volume-prefx", "VOLENV3": "volume-trim",
	"PANENV2": "pan", "PANENV": "pan-prefx",
	"WIDTHENV2": "width", "WIDTHENV": "width-prefx",
	"MUTEENV": "mute", "TEMPOENVEX": ir.TempoEnvelopeTarget, "MASTERPLAYSPEEDENV": "playrate",
}

func envelopeTarget(n *rpp.Node) string {
	tag := n.Tag()
	if tag == "PARMENV" {
		return "param:" + n.Arg(0)
	}
	if t, ok := envelopeTargets[tag]; ok {
		return t
	}
	return "reaper:" + tag
}

// viewState lists attributes that only describe how the editor looks:
// cursor, zoom, selection, window placement. They are carried through like
// any other data but are not reported as changes to the project.
var viewState = map[string]bool{
	"CURSOR": true, "ZOOM": true, "VZOOMEX": true, "SELECTION": true, "SELECTION2": true,
	"SEL": true, "TRACKHEIGHT": true, "WNDRECT": true, "SHOW": true, "LASTSEL": true,
	"DOCKED": true, "FLOATPOS": true, "FLOAT": true, "VIS": true, "LANEHEIGHT": true,
	"MIXERUIFLAGS": true, "SHOWINMIX": true, "MASTERTRACKHEIGHT": true, "MASTERTRACKVIEW": true,
	"MASTER_SEL": true, "TRACKSEL": true, "CFGEDIT": true, "CFGEDITVIEW": true, "EVTFILTER": true,
}

// isEnvelope recognizes automation blocks: VOLENV2, PANENV, PARMENV, TEMPOENVEX…
func isEnvelope(tag string) bool {
	for _, suffix := range []string{"ENV", "ENV2", "ENV3", "ENVEX"} {
		if strings.HasSuffix(tag, suffix) {
			return true
		}
	}
	return false
}

// text is a line without its indentation.
func text(n *rpp.Node) string { return strings.TrimLeft(n.Raw, " \t") }

// block is a whole block as indentation-free lines joined by "\n".
func block(n *rpp.Node) string {
	var lines []string
	collect(n, &lines)
	return strings.Join(lines, "\n")
}

// body is the content of a block without its opening and closing lines.
func body(n *rpp.Node) string {
	var lines []string
	for _, c := range n.Children {
		collect(c, &lines)
	}
	return strings.Join(lines, "\n")
}

func collect(n *rpp.Node, out *[]string) {
	*out = append(*out, text(n))
	if !n.Block {
		return
	}
	for _, c := range n.Children {
		collect(c, out)
	}
	*out = append(*out, ">")
}

func known(n *rpp.Node) ir.Entry {
	return ir.Entry{Key: n.Tag(), Raw: text(n), Known: true}
}

func opaque(n *rpp.Node) ir.Entry {
	if n.Block {
		return ir.Entry{Key: n.Tag(), Raw: block(n)}
	}
	return ir.Entry{Key: n.Tag(), Raw: text(n), View: viewState[n.Tag()]}
}

func num(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func integer(s string) int { return int(num(s)) }

func flag(s string) bool { return s != "" && s != "0" }
