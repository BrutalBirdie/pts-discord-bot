package runner

import (
	"fmt"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/BrutalBirdie/pts-discord-bot/internal/messages"
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

func TestButtonRows(t *testing.T) {
	var list []messages.Button
	for i := range 6 {
		list = append(list, messages.Button{Label: fmt.Sprint(i), TaskID: 9, Style: messages.StyleSuccess})
	}
	list = append(list, messages.Button{Label: "Rules", URL: "https://example.com", Emoji: "📜"})
	rows := buttonRows(4, list)
	if len(rows) != 2 {
		t.Fatalf("rows: %d", len(rows))
	}
	first, second := rows[0].(discord.ActionRowComponent), rows[1].(discord.ActionRowComponent)
	if len(first.Components) != 5 || len(second.Components) != 2 {
		t.Errorf("per row: %d %d", len(first.Components), len(second.Components))
	}
	b := second.Components[0].(discord.ButtonComponent)
	if b.Style != discord.ButtonStyleSuccess || b.CustomID != "task:9:4:5" {
		t.Errorf("task button: %+v", b)
	}
	link := second.Components[1].(discord.ButtonComponent)
	if link.Style != discord.ButtonStyleLink || link.URL != "https://example.com" || link.CustomID != "" || link.Emoji == nil || link.Emoji.Name != "📜" {
		t.Errorf("link button: %+v", link)
	}
	if task, msg, i, ok := ParseButtonID(b.CustomID); !ok || task != 9 || msg != 4 || i != 5 {
		t.Errorf("parse: %d %d %d %v", task, msg, i, ok)
	}
	if _, _, _, ok := ParseButtonID("task:0:1:2"); ok {
		t.Error("task 0 parsed")
	}
}
