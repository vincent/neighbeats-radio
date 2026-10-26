package speak

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func stub(writes string, err error, calls *int) func(context.Context, string, string, ...string) error {
	return func(_ context.Context, stdin, _ string, args ...string) error {
		*calls++
		if err != nil {
			return err
		}
		return os.WriteFile(args[len(args)-1], []byte(writes+stdin), 0o644)
	}
}

func TestRenderCachesByText(t *testing.T) {
	var calls int
	r := &Renderer{Piper: "piper", Model: "m.onnx", Dir: filepath.Join(t.TempDir(), "voice"), run: stub("RIFF", nil, &calls)}
	a, err := r.Render(context.Background(), "hello there")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Render(context.Background(), "hello there")
	c, _ := r.Render(context.Background(), "something else")
	if a != b || a == c || calls != 2 {
		t.Fatalf("a=%s b=%s c=%s calls=%d", a, b, c, calls)
	}
	if _, err := os.Stat(a + ".part"); err == nil {
		t.Fatal("temp file left behind")
	}
}

func TestRenderFailuresReturnErrors(t *testing.T) {
	var calls int
	dir := t.TempDir()
	r := &Renderer{Dir: dir, run: stub("", errors.New("no such file"), &calls)}
	if _, err := r.Render(context.Background(), "hi"); err == nil {
		t.Fatal("want error when piper fails")
	}
	r.run = func(_ context.Context, _, _ string, args ...string) error { return nil } // wrote nothing
	if _, err := r.Render(context.Background(), "hi"); err == nil {
		t.Fatal("want error when no audio is written")
	}
	if _, err := r.Render(context.Background(), "  "); err == nil {
		t.Fatal("want error on empty text")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestRenderTimeout(t *testing.T) {
	r := &Renderer{Dir: t.TempDir(), Timeout: 20 * time.Millisecond, run: func(ctx context.Context, _, _ string, _ ...string) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	start := time.Now()
	if _, err := r.Render(context.Background(), "slow"); err == nil || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestRenderPrunesOldFiles(t *testing.T) {
	var calls int
	dir := t.TempDir()
	old := filepath.Join(dir, "old.wav")
	os.WriteFile(old, []byte("x"), 0o644)
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(old, past, past)
	r := &Renderer{Dir: dir, KeepFor: time.Hour, run: stub("RIFF", nil, &calls)}
	if _, err := r.Render(context.Background(), "new"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("old file kept")
	}
}

func TestTemplatesVariantsNeverRepeatAndSayTable(t *testing.T) {
	fsys := fstest.MapFS{"between.tmpl": {Data: []byte("A {{.Next.Artist}}\n---\nB {{.Next.Artist}}")}}
	tp, err := LoadTemplates(fsys, map[string]string{"Sigur Rós": "Sigur Ross"})
	if err != nil {
		t.Fatal(err)
	}
	tp.rand = func(int) int { return 0 } // would pick A every time
	d := Data{Next: Track{Artist: "Sigur Rós"}}
	var got []string
	for i := 0; i < 4; i++ {
		s, err := tp.Text(Between, d)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	if got[0] != "A Sigur Ross" || got[1] != "B Sigur Ross" || got[2] != "A Sigur Ross" {
		t.Fatalf("got %v", got)
	}
	if s, _ := tp.Text(Hour, d); s != "" {
		t.Fatalf("moment without templates should say nothing, got %q", s)
	}
}

func TestBuiltinTemplatesAllExecute(t *testing.T) {
	tp, err := LoadTemplates(BuiltinFS(), nil)
	if err != nil {
		t.Fatal(err)
	}
	d := Data{Prev: Track{"P", "pt"}, Next: Track{"N", "nt"}, Dest: Track{"D", "dt"}, Time: "3:00 PM"}
	for m, vs := range tp.byName {
		if len(vs) < 2 {
			t.Errorf("%s: want several variants", m)
		}
		for range vs {
			s, err := tp.Text(m, d)
			if err != nil || s == "" || strings.Contains(s, "<no value>") {
				t.Errorf("%s: %q %v", m, s, err)
			}
		}
	}
	for _, m := range []string{Between, Hour, Steer, Leg} {
		if len(tp.byName[m]) == 0 {
			t.Errorf("missing builtin %s", m)
		}
	}
}
