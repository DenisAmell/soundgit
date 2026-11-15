package rpp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DenisAmell/soundgit/core/internal/testutil"
)

func TestRoundTripCorpus(t *testing.T) {
	for _, path := range testutil.Corpus(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := Parse(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := f.Bytes(); !bytes.Equal(got, data) {
				t.Fatalf("round trip differs: %d bytes in, %d bytes out", len(data), len(got))
			}
		})
	}
}

func TestRoundTripKeepsOddFormatting(t *testing.T) {
	cases := map[string]string{
		"crlf":           "<REAPER_PROJECT 0.1\r\n  TEMPO 120 4 4\r\n>\r\n",
		"mixed endings":  "<REAPER_PROJECT 0.1\r\n  TEMPO 120 4 4\n>\r\n",
		"no final eol":   "<REAPER_PROJECT 0.1\n  TEMPO 120 4 4\n>",
		"tabs and blank": "<REAPER_PROJECT 0.1\n\tTEMPO 120 4 4  \n\n  >\n\n",
		"bom":            "\xEF\xBB\xBF<REAPER_PROJECT 0.1\n>\n",
		"unknown block":  "<REAPER_PROJECT 0.1\n  <EXT_SWS \"a b\"\n    |free text <not a tag\n    QUJD\n  >\n  WEIRD 'x' `y` \"z\n>\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := Parse([]byte(src))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(f.Bytes()); got != src {
				t.Fatalf("got %q, want %q", got, src)
			}
		})
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []Token
	}{
		{`NAME Bass`, []Token{{"NAME", 0}, {"Bass", 0}}},
		{`NAME "Kick take"`, []Token{{"NAME", 0}, {"Kick take", '"'}}},
		{`NAME 'say "hi"'`, []Token{{"NAME", 0}, {`say "hi"`, '\''}}},
		{"NAME `both \" and '`", []Token{{"NAME", 0}, {`both " and '`, '`'}}},
		{`RECORD_PATH "Media" ""`, []Token{{"RECORD_PATH", 0}, {"Media", '"'}, {"", '"'}}},
		{`AUXRECV 1 -1:U ''`, []Token{{"AUXRECV", 0}, {"1", 0}, {"-1:U", 0}, {"", '\''}}},
		{"  PT\t0   1 ", []Token{{"PT", 0}, {"0", 0}, {"1", 0}}},
		{`NAME "unterminated`, []Token{{"NAME", 0}, {"unterminated", '"'}}},
		{``, nil},
	}
	for _, c := range cases {
		if got := Tokenize(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokenize(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestFormatTokenChoosesSafeQuote(t *testing.T) {
	cases := []struct {
		tok  Token
		want string
	}{
		{Token{"Bass", 0}, `Bass`},
		{Token{"Bass", '"'}, `"Bass"`},
		{Token{"two words", 0}, `"two words"`},
		{Token{"", 0}, `""`},
		{Token{`say "hi"`, '"'}, `'say "hi"'`},
		{Token{`"a" 'b'`, 0}, "`\"a\" 'b'`"},
		{Token{"\"a\" 'b' `c`", 0}, "`\"a\" 'b' 'c'`"},
	}
	for _, c := range cases {
		if got := FormatToken(c.tok); got != c.want {
			t.Errorf("FormatToken(%#v) = %s, want %s", c.tok, got, c.want)
		}
	}
	// Whatever is formatted must read back as the same text.
	for _, text := range []string{"plain", "two words", `q"uote`, "a'b", "", "'lead"} {
		back := Tokenize(FormatToken(Token{Text: text}))
		if len(back) != 1 || back[0].Text != text {
			t.Errorf("%q does not survive format+tokenize: %#v", text, back)
		}
	}
}

func TestModifiedLineIsReformattedOthersUntouched(t *testing.T) {
	src := "<REAPER_PROJECT 0.1\n  <TRACK\n    NAME   Bass\n    VOLPAN 1 0\n  >\n>\n"
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	name := f.Root.Children[0].Children[0]
	name.SetTokens([]Token{{Text: "NAME"}, {Text: "Sub Bass"}})
	want := "<REAPER_PROJECT 0.1\n  <TRACK\n    NAME \"Sub Bass\"\n    VOLPAN 1 0\n  >\n>\n"
	if got := string(f.Bytes()); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, src string
		line      int
		contains  string
	}{
		{"unclosed block", "<REAPER_PROJECT 0.1\n  <TRACK\n    NAME a\n>\n", 1, "never closed"},
		{"unclosed inner", "<REAPER_PROJECT 0.1\n  <TRACK\n    <ITEM\n  >\n>\n", 1, "never closed"},
		{"extra close", "<REAPER_PROJECT 0.1\n>\n>\n", 3, "without an open block"},
		{"not reaper", "{\"json\": true}\n", 1, "not a Reaper project"},
		{"other root", "<SOMETHING 1\n>\n", 1, "not a Reaper project"},
		{"empty", "", 1, "not a Reaper project"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := Parse([]byte(c.src))
			if err == nil {
				t.Fatal("expected an error")
			}
			if f != nil {
				t.Fatal("a failed parse must not return a tree")
			}
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("want *ParseError, got %T", err)
			}
			if pe.Line != c.line || !strings.Contains(pe.Msg, c.contains) {
				t.Fatalf("got line %d %q, want line %d containing %q", pe.Line, pe.Msg, c.line, c.contains)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, path := range testutil.Corpus(f) {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte("<REAPER_PROJECT\n<\n>\n>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := Parse(data)
		if err != nil {
			return
		}
		if got := file.Bytes(); !bytes.Equal(got, data) {
			t.Fatalf("parsed input does not round trip")
		}
	})
}

func BenchmarkParse(b *testing.B) {
	data := testutil.ReadFixture(b, "full.rpp")
	big := bytes.Repeat(data, 1) // real corpus files replace this once available
	b.SetBytes(int64(len(big)))
	for i := 0; i < b.N; i++ {
		if _, err := Parse(big); err != nil {
			b.Fatal(err)
		}
	}
}
