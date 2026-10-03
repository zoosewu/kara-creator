package app

import (
	"context"

	"github.com/zoosewu/kara-creator/nas/internal/download"
	"github.com/zoosewu/kara-creator/nas/internal/jobs"
	"github.com/zoosewu/kara-creator/nas/internal/pipeline"
)

// runner 是工作佇列實際做事的地方。
type runner struct{ a *App }

func (r *runner) Download(ctx context.Context, job jobs.Job, env jobs.Env) (string, string, error) {
	res, err := r.a.Down.Download(ctx, download.Request{URL: job.URL, AudioOnly: job.Options.AudioOnly, Folder: job.Options.Folder,
		Lyrics: job.Options.Lyrics, Log: env.Log, Progress: env.Progress})
	if err != nil {
		return "", "", err
	}
	r.a.invalidate(res.ID)
	return res.ID, res.Title, nil
}

func (r *runner) Process(ctx context.Context, job jobs.Job, env jobs.Env) error {
	run := pipeline.Run{Log: env.Log, Progress: env.Progress, Stage: env.Stage, Priority: job.Priority}
	p, id, opt := r.a.Pipe, job.Song, job.Options
	defer r.a.invalidate(id)
	for _, step := range job.Steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		switch step {
		case jobs.StepSeparate:
			err = p.Separate(ctx, id, opt.Force, run)
		case jobs.StepKaraoke:
			err = p.Karaoke(ctx, id, pipeline.KaraokeOptions{Force: opt.Force, Realign: opt.Realign}, run)
		case jobs.StepCheck:
			err = p.Check(ctx, id, opt.Force, run)
		case jobs.StepRetime:
			err = p.Retime(ctx, id, opt.Line, opt.Mode, run)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Finished 處理佇列的工作結束後同步 export/（先同步再標記結束，讓「完成」時輸出檔已經就位）。
func (r *runner) Finished(job jobs.Job, env jobs.Env) {
	res, err := r.a.syncExport()
	if err != nil {
		env.Log("同步匯出資料夾失敗：" + err.Error())
		return
	}
	for _, rel := range res.Added {
		env.Log("輸出：export/" + rel)
	}
	for _, rel := range res.Skipped {
		env.Log("輸出略過（已有同名檔案）：export/" + rel)
	}
}
