// Package ir is the normalized project model that comparison and merging
// work on. Typed fields carry meaning that is common to DAWs. Everything a
// DAW adapter does not understand travels in Ext, attached to the entity it
// belongs to, so that writing the model back loses nothing.
package ir

// Version of the model layout. Stored in every snapshot.
const Version = 1

// Entry is one element of an entity's original layout: a line or a nested
// block of the source format, in source order.
//
//   - Known is set when typed fields of the entity are read from this line;
//     Raw then holds the original text, reused verbatim while those fields
//     are unchanged.
//   - Key starting with "@" marks the position of child entities (Ref names
//     one of them when there are several of that kind).
//   - Otherwise the entry is opaque: Raw is carried through untouched.
type Entry struct {
	Key   string `json:"k"`
	Raw   string `json:"r,omitempty"`
	Ref   string `json:"ref,omitempty"`
	Known bool   `json:"known,omitempty"`
	View  bool   `json:"view,omitempty"` // opaque editor state (cursor, zoom, selection): not a change to the music
}

// Ext is adapter-specific passthrough data of one entity.
type Ext struct {
	Open   string  `json:"open,omitempty"` // original opening line of the entity's block
	Layout []Entry `json:"layout,omitempty"`
}

// Meta describes where the project came from.
type Meta struct {
	Daw        string  `json:"daw"`
	DawVersion string  `json:"dawVersion,omitempty"`
	SampleRate float64 `json:"sampleRate,omitempty"`
}

// Format records file-level details needed to write the project back.
type Format struct {
	EOL          string `json:"eol"`
	FinalNewline bool   `json:"finalNewline"`
	BOM          bool   `json:"bom,omitempty"`
}

// Tempo is the project's base tempo and time signature.
type Tempo struct {
	BPM float64 `json:"bpm"`
	Num int     `json:"num"`
	Den int     `json:"den"`
}

// Marker is a marker or one edge of a region.
type Marker struct {
	Index  int     `json:"index"`
	Time   float64 `json:"time"`
	Name   string  `json:"name"`
	Region bool    `json:"region,omitempty"`
	Raw    string  `json:"raw,omitempty"`
}

// Project is the root of the model.
type Project struct {
	IRVersion  int                  `json:"irVersion"`
	Meta       Meta                 `json:"meta"`
	Format     Format               `json:"format"`
	Tempo      Tempo                `json:"tempo"`
	Markers    []Marker             `json:"markers,omitempty"`
	Envelopes  map[string]*Envelope `json:"envelopes,omitempty"` // project-level, including the tempo map
	TrackOrder []string             `json:"trackOrder"`
	Tracks     map[string]*Track    `json:"tracks,omitempty"`
	Ext        Ext                  `json:"ext"`
}

// TempoEnvelopeTarget is the Target of the envelope that holds the tempo map.
const TempoEnvelopeTarget = "tempo"

// TempoMap returns the tempo envelope, or nil when the tempo is constant.
func (p *Project) TempoMap() *Envelope {
	for _, e := range p.Envelopes {
		if e.Target == TempoEnvelopeTarget {
			return e
		}
	}
	return nil
}

// Receive is a send arriving at a track from another track.
type Receive struct {
	SrcTrackID string  `json:"srcTrackId"`
	Mode       int     `json:"mode"`
	Volume     float64 `json:"volume"`
	Pan        float64 `json:"pan"`
	Raw        string  `json:"raw,omitempty"`
}

// Track is a mixer channel with its content.
type Track struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Color       int                  `json:"color"`
	Volume      float64              `json:"volume"`
	Pan         float64              `json:"pan"`
	Mute        bool                 `json:"mute"`
	Solo        int                  `json:"solo"`
	Phase       bool                 `json:"phase"`
	FolderDepth int                  `json:"folderDepth"` // +1 opens a folder, negative closes that many
	Receives    []Receive            `json:"receives,omitempty"`
	FxOrder     []string             `json:"fxOrder,omitempty"`
	Fx          map[string]*Fx       `json:"fx,omitempty"`
	FxChainExt  Ext                  `json:"fxChainExt"`
	Envelopes   map[string]*Envelope `json:"envelopes,omitempty"`
	ItemOrder   []string             `json:"itemOrder,omitempty"`
	Items       map[string]*Item     `json:"items,omitempty"`
	Ext         Ext                  `json:"ext"`
}

// Fx is one effect in a chain. Its state is opaque: State holds the plugin's
// saved data and travels as a separate object, StateHash identifies it.
type Fx struct {
	ID        string               `json:"id"`
	Format    string               `json:"format"` // VST, VST3, AU, JS, CLAP, ...
	Name      string               `json:"name"`
	Ident     string               `json:"ident"`
	Bypass    bool                 `json:"bypass"`
	Offline   bool                 `json:"offline"`
	State     string               `json:"-"`
	StateHash string               `json:"stateHash"`
	Envelopes map[string]*Envelope `json:"envelopes,omitempty"`
	Ext       Ext                  `json:"ext"`
}

// Point is one automation point.
type Point struct {
	Time  float64 `json:"t"`
	Value float64 `json:"v"`
	Shape int     `json:"s"`
	Raw   string  `json:"raw,omitempty"`
}

// Envelope is an automation lane.
type Envelope struct {
	ID     string  `json:"id"`
	Target string  `json:"target"` // volume, pan, mute, width, tempo, playrate, param:<id>, or <daw>:<tag>
	Active bool    `json:"active"`
	Points []Point `json:"points,omitempty"`
	Ext    Ext     `json:"ext"`
}

// Item is a clip on a track's timeline.
type Item struct {
	ID         string  `json:"id"`
	Position   float64 `json:"position"`
	Length     float64 `json:"length"`
	Loop       bool    `json:"loop"`
	FadeIn     float64 `json:"fadeIn"`
	FadeOut    float64 `json:"fadeOut"`
	Mute       bool    `json:"mute"`
	ActiveTake int     `json:"activeTake"`
	Takes      []*Take `json:"takes,omitempty"`
	Ext        Ext     `json:"ext"`
}

// Take is one alternative content of an item.
type Take struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Offset   float64 `json:"offset"`
	Playrate float64 `json:"playrate"`
	Pitch    float64 `json:"pitch"`
	Source   *Source `json:"source,omitempty"`
	Ext      Ext     `json:"ext"`
}

// Source is what a take plays: an audio file, MIDI data, or a section of
// another source.
type Source struct {
	Type  string  `json:"type"`
	File  string  `json:"file,omitempty"` // normalized asset path, see NormalizePath
	Midi  *Midi   `json:"midi,omitempty"`
	Inner *Source `json:"inner,omitempty"`
	Ext   Ext     `json:"ext"`
}

// Midi is the content of a MIDI source. Events are kept in the adapter's
// event notation, one per element, in order.
type Midi struct {
	PPQ    int      `json:"ppq"`
	Events []string `json:"events,omitempty"`
}

// AssetPaths lists every file the project references, in a stable order
// without duplicates.
func (p *Project) AssetPaths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s *Source) {
		for ; s != nil; s = s.Inner {
			if s.File != "" && !seen[s.File] {
				seen[s.File] = true
				out = append(out, s.File)
			}
		}
	}
	for _, tid := range p.TrackOrder {
		t := p.Tracks[tid]
		if t == nil {
			continue
		}
		for _, iid := range t.ItemOrder {
			if it := t.Items[iid]; it != nil {
				for _, tk := range it.Takes {
					add(tk.Source)
				}
			}
		}
	}
	return out
}
