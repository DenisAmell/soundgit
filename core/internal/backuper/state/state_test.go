package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStateSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Update(func(d *Data) {
		d.Accounts["a"] = &Account{ID: "a", Provider: "yandex", Status: AccountOK}
		d.Watches["w"] = &Watch{ID: "w", LocalPath: "/renders", AccountID: "a", Files: map[string]*File{
			"mix.wav": {Status: FileUploaded, Size: 3, Hash: "h", Pending: &Pending{Size: 4, Attempts: 2}},
		}}
		d.AddEvent(Event{File: "mix.wav", Kind: EventUploaded})
	})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	again.View(func(d *Data) {
		f := d.Watches["w"].Files["mix.wav"]
		if d.Accounts["a"].Provider != "yandex" || f.Hash != "h" || f.Pending.Attempts != 2 || len(d.Recent) != 1 {
			t.Errorf("reopened state differs: %+v / %+v", f, d.Recent)
		}
	})
	if st, _ := os.Stat(filepath.Join(dir, "state.json")); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("state.json is readable by others: %v", st.Mode().Perm())
	}
}

func TestUnreadableStateIsSetAside(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "state.json"), []byte("{broken"), 0o600)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.View(func(d *Data) {
		if len(d.Watches) != 0 {
			t.Error("state is not empty")
		}
	})
	if _, err := os.Stat(filepath.Join(dir, "state.json.unreadable")); err != nil {
		t.Error("the broken file was not kept for inspection")
	}
}

// Workers finish uploads at the same moment and each saves. Whatever the
// interleaving, the file must end up holding everything.
func TestConcurrentSavesKeepTheLatestState(t *testing.T) {
	for round := 0; round < 40; round++ {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.Update(func(d *Data) { d.Watches["w"] = &Watch{ID: "w", Files: map[string]*File{}} })
		const n = 16
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				s.Update(func(d *Data) { d.Watches["w"].Files[fmt.Sprintf("f%02d.wav", i)] = &File{Status: FileUploaded} })
				if err := s.Save(); err != nil {
					t.Error(err)
				}
			}(i)
		}
		wg.Wait()
		again, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		again.View(func(d *Data) {
			if got := len(d.Watches["w"].Files); got != n {
				t.Fatalf("round %d: the file on disk holds %d of %d records", round, got, n)
			}
		})
	}
}
