package runner

import (
	"testing"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

func TestRunLimit(t *testing.T) {
	lengths := map[int64]time.Duration{1: 30 * time.Second, 2: 90 * time.Second}
	soundTime := func(ref tasks.SoundRef) time.Duration { return lengths[ref.MediaID] }
	maxPlay := 10 * time.Minute
	join := tasks.Step{Type: tasks.Join}
	play := func(order string, ids ...int64) tasks.Step {
		return tasks.Step{Type: tasks.Play, Order: order, MediaIDs: ids}
	}
	for name, c := range map[string]struct {
		steps []tasks.Step
		want  time.Duration
	}{
		"no steps":        {nil, runSlack},
		"message":         {[]tasks.Step{{Type: tasks.Message}}, runSlack},
		"join and play":   {[]tasks.Step{join, play("", 1)}, runSlack + joinTime + 30*time.Second + startTime},
		"sequence":        {[]tasks.Step{play(tasks.OrderSequence, 1, 2)}, runSlack + 120*time.Second + 2*startTime},
		"random":          {[]tasks.Step{play(tasks.OrderRandom, 1, 2)}, runSlack + 90*time.Second + startTime},
		"unknown length":  {[]tasks.Step{play("", 3)}, runSlack + maxPlay},
		"tag":             {[]tasks.Step{{Type: tasks.Play, Tag: "intro"}}, runSlack + maxPlay},
		"longer than max": {[]tasks.Step{play("", 1, 3)}, runSlack + maxPlay},
		"wait":            {[]tasks.Step{{Type: tasks.Wait, WaitMS: 5000}, {Type: tasks.Leave}}, runSlack + 5*time.Second},
		"profile":         {[]tasks.Step{{Type: tasks.Profile}}, runSlack + 2*tasks.ProfileMaxWait},
		"capped":          {[]tasks.Step{{Type: tasks.Wait, Seconds: 86400}, {Type: tasks.Wait, Seconds: 86400}}, maxRunLimit},
	} {
		if got := runLimit(c.steps, soundTime, maxPlay); got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
}
