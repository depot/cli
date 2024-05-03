package progresshelper

import (
	"github.com/docker/buildx/util/progress"
	"github.com/moby/buildkit/client"
)

type tee struct {
	progress.Writer
	ch chan *client.SolveStatus
}

func (t *tee) Write(v *client.SolveStatus) {
	v2 := *v
	t.ch <- &v2
	t.Writer.Write(v)
}

func Tee(w progress.Writer, ch chan *client.SolveStatus) progress.Writer {
	if ch == nil {
		return w
	}
	return &tee{
		Writer: w,
		ch:     ch,
	}
}
