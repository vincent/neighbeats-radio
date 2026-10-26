package director

import (
	"context"

	"github.com/vincent/neighbeats-radio/internal/plan"
	"github.com/vincent/neighbeats-radio/internal/speak"
)

// Speaker renders text to a WAV file. Errors mean "no break": the caller carries on.
type Speaker interface {
	Render(ctx context.Context, text string) (string, error)
}

// Words writes the sentence for a moment; "" means nothing to say.
type Words interface {
	Text(moment string, d speak.Data) (string, error)
}

// Voice is the director's spoken-break machinery.
type Voice struct {
	Breaks  *Breaks
	Speaker Speaker
	Words   Words
}

// voiceJob is a break being rendered for one pushed track. file is valid once done
// is closed and err is nil.
type voiceJob struct {
	done chan struct{}
	file string
}

func (j *voiceJob) ready() (string, bool) {
	select {
	case <-j.done:
		return j.file, j.file != ""
	default:
		return "", false
	}
}

func toSpeak(t plan.Track) speak.Track { return speak.Track{Artist: t.Artist, Title: t.Title} }

// planBreak runs when track it has been handed to Liquidsoap. If a break is due it
// starts rendering in the background, so planning and pushing never wait for Piper.
// The text names the track before it in the plan and the track itself, so it stays
// right after skips and steers: both rewrite the plan before anything is pushed.
func (d *Director) planBreak(ctx context.Context, it plan.Item) {
	v := d.cfg.Voice
	if v == nil || v.Breaks == nil {
		return
	}
	prev, ok := d.planner.Before(it.Path)
	if !ok {
		return // first track after a start: nothing to follow
	}
	legStart := prev.Leg != it.Leg
	steer := d.steerLeg != 0 && it.Leg == d.steerLeg
	if steer {
		d.steerLeg = 0
	}
	moment := v.Breaks.Decide(Event{Now: d.now(), Steer: steer, LegStart: legStart})
	if moment == "" {
		return
	}
	data := speak.Data{Prev: toSpeak(prev.Track), Next: toSpeak(it.Track), Time: d.now().Format("3:04 PM")}
	if end, ok := d.planner.LegEnd(it.Leg); ok {
		data.Dest = toSpeak(end.Track)
	}
	text, err := v.Words.Text(moment, data)
	if err != nil || text == "" {
		if err != nil {
			d.log.Warn("break text", "moment", moment, "err", err)
		}
		return
	}
	job := &voiceJob{done: make(chan struct{})}
	d.voices[it.Path] = job
	go func() {
		defer close(job.done)
		file, err := v.Speaker.Render(ctx, text)
		if err != nil {
			d.log.Warn("break not rendered, music goes on", "moment", moment, "text", text, "err", err)
			return
		}
		job.file = file
		d.log.Info("break ready", "moment", moment, "text", text)
	}()
}

// sayBreak runs when path starts playing: the break for it, if rendered in time, is
// queued now so it lands over the start of the track. A late render is dropped.
func (d *Director) sayBreak(ctx context.Context, path string) {
	for p, job := range d.voices {
		if p != path {
			// Breaks for tracks that were dropped or skipped never play.
			if !d.stillPlanned(p) {
				delete(d.voices, p)
			}
		} else {
			delete(d.voices, p)
			if file, ok := job.ready(); ok {
				if err := d.lq.PushVoice(ctx, file); err != nil {
					d.log.Warn("push voice", "err", err)
				}
			} else {
				d.log.Warn("break not ready in time, dropped", "track", path)
			}
		}
	}
}

func (d *Director) stillPlanned(path string) bool {
	for _, it := range d.planner.Items() {
		if it.Path == path {
			return true
		}
	}
	return false
}
