// Package speak turns text into spoken WAV files with Piper and writes the words:
// templates with several variants per moment, and a table for names Piper mangles.
package speak

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"
)

// Renderer runs Piper once per sentence. Files are named after the hash of the
// text, so a repeated sentence (an ident) is rendered once. Any failure is an
// error for the caller to swallow: a voice never holds up the music.
type Renderer struct {
	Piper   string // the piper binary
	Model   string // the .onnx voice
	Dir     string // where the WAVs go, shared with Liquidsoap
	Timeout time.Duration
	// KeepFor is how long a rendered file stays before the next Render deletes it.
	KeepFor time.Duration

	// run returns the tail of stderr along with any error.
	run func(ctx context.Context, stdin string, name string, args ...string) (string, error)
}

// Render returns the path of a WAV saying text.
func (r *Renderer) Render(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("speak: empty text")
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return "", fmt.Errorf("speak: %w", err)
	}
	sum := sha256.Sum256([]byte(r.Model + "\x00" + text))
	path := filepath.Join(r.Dir, hex.EncodeToString(sum[:8])+".wav")
	if _, err := os.Stat(path); err == nil {
		now := time.Now()
		os.Chtimes(path, now, now)
		return path, nil
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	run := r.run
	if run == nil {
		run = runCommand
	}
	tmp := path + ".part"
	defer os.Remove(tmp)
	stderr, err := run(ctx, text, r.Piper, "--model", r.Model, "--output_file", tmp)
	if err != nil {
		return "", fmt.Errorf("speak: %w", err)
	}
	if fi, err := os.Stat(tmp); err != nil || fi.Size() == 0 {
		return "", fmt.Errorf("speak: piper wrote no audio: %s", stderr)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("speak: %w", err)
	}
	r.prune()
	return path, nil
}

// prune deletes files older than KeepFor; Liquidsoap has long finished with them.
func (r *Renderer) prune() {
	if r.KeepFor <= 0 {
		return
	}
	entries, _ := os.ReadDir(r.Dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && !e.IsDir() && time.Since(fi.ModTime()) > r.KeepFor {
			os.Remove(filepath.Join(r.Dir, e.Name()))
		}
	}
}

func runCommand(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > 200 {
		msg = msg[len(msg)-200:]
	}
	if err != nil {
		return msg, fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return msg, nil
}

// Moments a break can be about.
const (
	Between = "between" // after N tracks: what was, what is next
	Hour    = "hour"    // time check
	Steer   = "steer"   // the listener changed course
	Leg     = "leg"     // a new leg starts: where the route heads
)

// Track is what a template may say about a track.
type Track struct{ Artist, Title string }

// Data is what templates receive.
type Data struct {
	Prev, Next, Dest Track
	Time             string // "3:04 PM"
}

//go:embed templates/en/*.tmpl
var builtin embed.FS

// Templates picks and fills the words. Each moment has a file with variants
// separated by a line holding only `---`.
type Templates struct {
	say     *strings.Replacer
	byName  map[string][]*template.Template
	mu      sync.Mutex
	lastIdx map[string]int
	rand    func(n int) int
}

// BuiltinFS is the embedded English set, rooted so files read as <moment>.tmpl.
func BuiltinFS() fs.FS {
	sub, _ := fs.Sub(builtin, "templates/en")
	return sub
}

// LoadTemplates reads <moment>.tmpl files from fsys. say maps a written name to
// how Piper should be told to say it. A missing moment is fine: no break for it.
func LoadTemplates(fsys fs.FS, say map[string]string) (*Templates, error) {
	t := &Templates{byName: map[string][]*template.Template{}, lastIdx: map[string]int{}, rand: rand.IntN}
	var pairs []string
	for k, v := range say {
		pairs = append(pairs, k, v)
	}
	t.say = strings.NewReplacer(pairs...)
	for _, m := range []string{Between, Hour, Steer, Leg} {
		raw, err := fs.ReadFile(fsys, m+".tmpl")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("speak: %w", err)
		}
		for i, v := range splitVariants(string(raw)) {
			tpl, err := template.New(fmt.Sprintf("%s#%d", m, i)).Option("missingkey=error").Parse(v)
			if err != nil {
				return nil, fmt.Errorf("speak: %w", err)
			}
			t.byName[m] = append(t.byName[m], tpl)
		}
	}
	return t, nil
}

func splitVariants(s string) []string {
	var out []string
	for _, v := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n---\n") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// Text writes one sentence for the moment, never the same variant twice in a row.
// It returns "" when the moment has no templates.
func (t *Templates) Text(moment string, d Data) (string, error) {
	variants := t.byName[moment]
	if len(variants) == 0 {
		return "", nil
	}
	t.mu.Lock()
	i := t.rand(len(variants))
	if last, ok := t.lastIdx[moment]; ok && len(variants) > 1 && i == last {
		i = (i + 1) % len(variants)
	}
	t.lastIdx[moment] = i
	t.mu.Unlock()

	d.Prev, d.Next, d.Dest = t.mouth(d.Prev), t.mouth(d.Next), t.mouth(d.Dest)
	var b strings.Builder
	if err := variants[i].Execute(&b, d); err != nil {
		return "", fmt.Errorf("speak: %w", err)
	}
	return strings.Join(strings.Fields(b.String()), " "), nil
}

func (t *Templates) mouth(tr Track) Track {
	return Track{Artist: t.say.Replace(tr.Artist), Title: t.say.Replace(tr.Title)}
}
