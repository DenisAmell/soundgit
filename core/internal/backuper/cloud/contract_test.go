package cloud_test

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/cloudtest"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/gdrive"
	"github.com/DenisAmell/soundgit/core/internal/backuper/cloud/yandex"
)

// backend is one cloud under test: the real client talking to a fake server.
type backend struct {
	name     string
	provider cloud.Provider
	faults   *cloudtest.Faults
	file     func(p string) ([]byte, bool) // what the cloud holds, by client path
	paths    func() []string
	put      func(p string, data []byte)
	quota    func(int64)
	revoke   func()
}

func backends(t *testing.T) []backend {
	t.Helper()
	y := cloudtest.NewYandex()
	t.Cleanup(y.Server.Close)
	// The clients hold the token issued at sign-in; revoking it in the cloud
	// must not hand them a new one.
	yTok, gTok := y.Token(), ""
	yc := yandex.New(func(context.Context) (string, error) { return yTok, nil })
	yc.BaseURL = y.Server.URL

	g := cloudtest.NewGDrive()
	t.Cleanup(g.Server.Close)
	gTok = g.Token()
	gc := gdrive.New(func(context.Context) (string, error) { return gTok, nil })
	gc.BaseURL, gc.RetryDelay = g.Server.URL, time.Millisecond
	root := gdrive.DefaultRoot + "/"
	strip := func(ps []string) []string {
		for i := range ps {
			ps[i] = strings.TrimPrefix(ps[i], root)
		}
		return ps
	}
	return []backend{
		{"yandex", yc, &y.Faults, y.File, y.Paths, y.Put, y.SetQuota, y.RevokeToken},
		{"gdrive", gc, &g.Faults,
			func(p string) ([]byte, bool) { return g.File(root + p) },
			func() []string { return strip(g.Paths()) },
			func(p string, d []byte) { g.Put(root+p, d) }, g.SetQuota, g.RevokeToken},
	}
}

func md5hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

func each(t *testing.T, fn func(t *testing.T, b backend)) {
	for _, b := range backends(t) {
		t.Run(b.name, func(t *testing.T) { fn(t, b) })
	}
}

var ctx = context.Background()

func TestUploadStatAndFolders(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		data := bytes.Repeat([]byte("audio"), 1000)
		const p = "Renders/Stems/Drums/kick.wav"
		var last int64
		e, err := b.provider.Upload(ctx, p, bytes.NewReader(data), int64(len(data)), false, func(n int64) { last = n })
		if err != nil {
			t.Fatal(err)
		}
		if e.Size != int64(len(data)) || e.MD5 != md5hex(data) {
			t.Errorf("entry = %+v", e)
		}
		if last != int64(len(data)) {
			t.Errorf("progress stopped at %d of %d", last, len(data))
		}
		if got, ok := b.file(p); !ok || !bytes.Equal(got, data) {
			t.Fatal("the cloud does not hold the uploaded bytes")
		}
		st, err := b.provider.Stat(ctx, p)
		if err != nil || st != e {
			t.Errorf("stat = %+v, %v; upload returned %+v", st, err, e)
		}
		if _, err := b.provider.Stat(ctx, "Renders/missing.wav"); !errors.Is(err, cloud.ErrNotFound) {
			t.Errorf("stat of a missing file: %v", err)
		}
		if _, err := b.provider.Stat(ctx, "No/Such/Folder/x.wav"); !errors.Is(err, cloud.ErrNotFound) {
			t.Errorf("stat in a missing folder: %v", err)
		}
	})
}

func TestNamesWithSpecialCharacters(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		for _, name := range []string{
			"Untitled_1#01 & микс+2.wav", "it's 100% done?.wav", `back\slash "quoted".wav`, "  spaced  name .wav",
		} {
			p := "Мои рендеры/" + name
			data := []byte("content of " + name)
			if _, err := b.provider.Upload(ctx, p, bytes.NewReader(data), int64(len(data)), false, nil); err != nil {
				t.Errorf("%q: %v", name, err)
				continue
			}
			if got, ok := b.file(p); !ok || !bytes.Equal(got, data) {
				t.Errorf("%q is not stored under its own name; cloud has %q", name, b.paths())
			}
			if _, err := b.provider.Stat(ctx, p); err != nil {
				t.Errorf("stat %q: %v", name, err)
			}
		}
	})
}

func TestOverwriteAndRename(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		up := func(p, content string, overwrite bool) error {
			_, err := b.provider.Upload(ctx, p, strings.NewReader(content), int64(len(content)), overwrite, nil)
			return err
		}
		if err := up("a/mix.wav", "v1", false); err != nil {
			t.Fatal(err)
		}
		if err := up("a/mix.wav", "v2", false); !errors.Is(err, cloud.ErrExists) {
			t.Fatalf("second upload without overwrite: %v", err)
		}
		if got, _ := b.file("a/mix.wav"); string(got) != "v1" {
			t.Fatal("a refused upload changed the file")
		}

		if err := b.provider.Rename(ctx, "a/mix.wav", "mix (old).wav"); err != nil {
			t.Fatal(err)
		}
		if err := up("a/mix.wav", "v2", false); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(b.paths(), "|"); got != "a/mix (old).wav|a/mix.wav" {
			t.Errorf("cloud holds %s", got)
		}
		if err := b.provider.Rename(ctx, "a/mix.wav", "mix (old).wav"); !errors.Is(err, cloud.ErrExists) {
			t.Errorf("rename onto an existing name: %v", err)
		}
		if err := b.provider.Rename(ctx, "a/none.wav", "x.wav"); !errors.Is(err, cloud.ErrNotFound) {
			t.Errorf("rename of a missing file: %v", err)
		}

		if err := up("a/mix.wav", "v3 longer", true); err != nil {
			t.Fatal(err)
		}
		if got, _ := b.file("a/mix.wav"); string(got) != "v3 longer" {
			t.Errorf("overwrite left %q", got)
		}
		if n := len(b.paths()); n != 2 {
			t.Errorf("overwrite created a duplicate: %v", b.paths())
		}
	})
}

func TestEmptyFile(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		e, err := b.provider.Upload(ctx, "empty.wav", strings.NewReader(""), 0, false, nil)
		if err != nil || e.Size != 0 || e.MD5 != md5hex(nil) {
			t.Fatalf("entry = %+v, %v", e, err)
		}
	})
}

func TestAccount(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		b.quota(1000)
		b.provider.Upload(ctx, "x.wav", strings.NewReader("12345"), 5, false, nil)
		a, err := b.provider.Account(ctx)
		if err != nil || a.Name == "" || a.TotalBytes != 1000 || a.FreeBytes != 995 {
			t.Fatalf("account = %+v, %v", a, err)
		}
	})
}

func TestErrorClasses(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		up := func() error {
			_, err := b.provider.Upload(ctx, "f/x.wav", strings.NewReader("12345678"), 8, true, nil)
			return err
		}
		if err := up(); err != nil {
			t.Fatal(err)
		}
		for status, want := range map[int]cloud.Kind{500: cloud.Temporary, 503: cloud.Temporary, 429: cloud.Temporary} {
			b.faults.FailNext(status, 1)
			if err := up(); err == nil || cloud.KindOf(err) != want {
				t.Errorf("HTTP %d: %v (kind %v), want kind %v", status, err, cloud.KindOf(err), want)
			}
		}

		b.quota(4)
		if err := up(); cloud.KindOf(err) != cloud.Quota {
			t.Errorf("full storage: %v (kind %v)", err, cloud.KindOf(err))
		}
		b.quota(0)

		b.revoke()
		if err := up(); cloud.KindOf(err) != cloud.Auth {
			t.Errorf("revoked token: %v (kind %v)", err, cloud.KindOf(err))
		}
		if _, err := b.provider.Account(ctx); cloud.KindOf(err) != cloud.Auth {
			t.Errorf("account with a revoked token: %v", err)
		}
	})
}

func TestDroppedConnectionIsTemporaryOrResumed(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		data := bytes.Repeat([]byte("0123456789"), 5000)
		b.faults.CutNextUpload(20_000)
		_, err := b.provider.Upload(ctx, "big.wav", bytes.NewReader(data), int64(len(data)), false, nil)
		if err != nil {
			// A provider without resume reports a retryable failure and leaves no file behind.
			if cloud.KindOf(err) != cloud.Temporary {
				t.Fatalf("dropped connection: %v (kind %v)", err, cloud.KindOf(err))
			}
			if _, ok := b.file("big.wav"); ok {
				t.Fatal("a failed upload left a file in the cloud")
			}
			if _, err = b.provider.Upload(ctx, "big.wav", bytes.NewReader(data), int64(len(data)), false, nil); err != nil {
				t.Fatalf("retry: %v", err)
			}
		}
		if got, ok := b.file("big.wav"); !ok || !bytes.Equal(got, data) {
			t.Fatal("the cloud does not hold the full file after the interruption")
		}
	})
}

func TestForeignFolderLayoutIsRespected(t *testing.T) {
	each(t, func(t *testing.T, b backend) {
		b.put("Shared/existing.wav", []byte("someone else's"))
		if _, err := b.provider.Upload(ctx, "Shared/existing.wav", strings.NewReader("mine"), 4, false, nil); !errors.Is(err, cloud.ErrExists) {
			t.Fatalf("upload over a foreign file: %v", err)
		}
		if _, err := b.provider.Upload(ctx, "Shared/new.wav", strings.NewReader("mine"), 4, false, nil); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(b.paths(), "|"); got != "Shared/existing.wav|Shared/new.wav" {
			t.Errorf("cloud holds %s", got)
		}
	})
}
