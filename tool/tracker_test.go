package kfeed

import "testing"

// TestTrackerSongsDone: a song counts as done when its media files are, not only after the txt files at the end.
func TestTrackerSongsDone(t *testing.T) {
	var last Progress
	tr := &tracker{fn: func(p Progress) { last = p }}
	a := &songPlan{}
	a.jobs = []*fileJob{{size: 10, plan: a}, {size: 5, plan: a}, {size: 1, txt: true, plan: a}}
	b := &songPlan{keep: map[string]fileRec{"b.mp3": {Size: 7}}}
	b.jobs = []*fileJob{{size: 1, txt: true, plan: b}}
	tr.start([]*songPlan{a, b})
	if last.SongsDone != 1 || last.SongsTotal != 2 || last.BytesDone != 7 || last.BytesTotal != 24 {
		t.Fatalf("start: %+v", last) // b has only its txt left: done
	}
	tr.processed(a.jobs[0])
	tr.send()
	if last.SongsDone != 1 {
		t.Fatalf("one of two media files: %+v", last)
	}
	tr.processed(a.jobs[1])
	tr.processed(a.jobs[2]) // the txt changes nothing
	tr.send()
	if last.SongsDone != 2 {
		t.Fatalf("media done: %+v", last)
	}
}
