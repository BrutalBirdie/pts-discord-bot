package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BrutalBirdie/pts-discord-bot/internal/bot"
	"github.com/BrutalBirdie/pts-discord-bot/internal/store"
	"github.com/BrutalBirdie/pts-discord-bot/internal/tasks"
)

// TestScreenshotRoom writes the soundboard of a made up server for the screenshots of test/e2e/run.sh,
// which has no Discord connection to show a real one.
func TestScreenshotRoom(t *testing.T) {
	dir := os.Getenv("SCREENSHOT_DIR")
	if dir == "" {
		t.Skip("SCREENSHOT_DIR is not set")
	}
	s := testServer(t)
	sess := &store.Session{Username: "ann", DisplayName: "ann", IsAdmin: true, CSRFToken: "tok"}

	member := func(id, name, status, group, color, voice string) bot.Member {
		m := bot.Member{ID: id, Name: name, Username: name, Status: status, Online: status != "offline", Group: group, Color: color}
		if voice != "" {
			m.VoiceID, m.Voice = "20", voice
		}
		return m
	}
	members := []bot.Member{
		member("1", "Robin", "online", "Admins", "#e67e22", "Lounge"),
		member("2", "Sam", "dnd", "Moderators", "#3498db", "Lounge"),
		member("3", "Kai", "idle", "Moderators", "#3498db", ""),
		member("4", "Alex", "online", "", "", "Lounge"),
		member("5", "Jo", "online", "", "", ""),
		member("6", "Mika", "online", "", "", ""),
		member("7", "Noa", "idle", "", "", ""),
		member("8", "Lee", "offline", "", "", ""),
		member("9", "Pat", "offline", "", "", ""),
	}
	members[0].GroupPos, members[1].GroupPos, members[2].GroupPos = 2, 1, 1
	members[3].Mobile = true
	members[4].Activity, members[4].Doing = "Minecraft", "playing"
	groups, hidden, more := memberList(members, "", 1, true)
	inVoice := []bot.Member{members[0], members[1], members[3]}

	lounge := bot.Channel{ID: "20", Name: "Lounge", Voice: true, Usable: true, HasBot: true, People: inVoice}
	guild := bot.Guild{ID: "1", Name: "Community", MemberCount: 164, BotVoice: "Lounge", Self: bot.Member{Name: "PTS Bot", Bot: true}}

	sound := func(id int64, name, emoji string, ms int, tags ...string) soundTile {
		return soundTile{Media: store.Media{ID: id, Name: name, Kind: store.KindSound, DurationMS: ms, Emoji: emoji, Tags: tags}}
	}
	now := time.Now()
	horn := tasks.Task{ID: 1, Name: "Raid horn", GuildID: "1", Enabled: true, Trigger: tasks.Trigger{Kind: tasks.Event, Event: tasks.VoiceJoin, Timezone: "Europe/Berlin"},
		Steps: []tasks.Step{{Type: tasks.Join, ToTrigger: true}, {Type: tasks.Play, MediaIDs: []int64{1}}, {Type: tasks.Leave}}}
	chime := tasks.Task{ID: 2, Name: "Morning chime", GuildID: "1", Enabled: true, Trigger: tasks.Trigger{Kind: tasks.Daily, Times: []string{"08:00", "12:00", "18:00"}, Timezone: "Europe/Berlin"},
		Steps: []tasks.Step{{Type: tasks.Join, ChannelID: "20"}, {Type: tasks.Play, MediaIDs: []int64{1}}, {Type: tasks.Leave}}}
	next := now.Add(11 * time.Hour)
	d := roomData{Guild: guild, View: "voice", Channel: lounge, Self: "/room/1?c=20", Paging: paging{Page: 1},
		Playing: []playingView{{P: bot.Playing{GuildID: "1", ChannelID: "20", Channel: "Lounge", Name: "airhorn.ogg", By: "Robin", Started: now.Add(-2 * time.Second), Duration: 6 * time.Second}, CSRF: "tok", Next: "/room/1?c=20"}},
		Favorites: []soundTile{
			{Media: sound(1, "airhorn.ogg", "📯", 6000, "fun").Media, Fav: true},
			{Media: sound(2, "drumroll.ogg", "🥁", 4000, "music").Media, Fav: true},
			{Media: sound(3, "applause.ogg", "👏", 5000, "fun").Media, Fav: true},
		},
		Own: []soundTile{sound(4, "raid-start.ogg", "⚔️", 3000, "alerts"), sound(5, "victory.ogg", "🏆", 7000, "music")},
		Rest: []soundTile{
			sound(6, "sad-trombone.ogg", "🎺", 3000, "fun"), sound(7, "doorbell.ogg", "🔔", 2000, "alerts"), sound(8, "rimshot.ogg", "😂", 2000, "fun"),
			sound(9, "cheer.ogg", "🎉", 4000, "fun"), sound(10, "level-up.ogg", "⭐", 2000, "music"), sound(11, "countdown.ogg", "⏱️", 10000, "alerts"),
			sound(12, "crickets.ogg", "🦗", 5000, "fun"), sound(13, "bell.ogg", "🛎️", 1000, "alerts"),
		},
		SoundTags: []store.SoundTag{{Name: "fun", Count: 6, Emoji: "🎉"}, {Name: "music", Count: 3, Emoji: "🎵"}, {Name: "alerts", Count: 4, Emoji: "🔔"}},
		Tasks:     []taskRow{{Task: chime, What: "join Lounge, play airhorn.ogg, leave", Next: &next}, {Task: horn, What: "join the member, play airhorn.ogg, leave"}},
	}
	sh := &shell{Guilds: []bot.Guild{guild, {ID: "2", Name: "Gaming"}, {ID: "3", Name: "Book Club"}}, Guild: guild, Path: d.Self, Channel: "20",
		Text: []roomChannel{
			{Channel: bot.Channel{ID: "10", Name: "general", Usable: true}},
			{Channel: bot.Channel{ID: "11", Name: "announcements", Usable: true}, Tasks: 1},
			{Channel: bot.Channel{ID: "12", Name: "raids", Usable: true}, Tasks: 1},
			{Channel: bot.Channel{ID: "13", Name: "memes", Usable: true}},
		},
		Voice: []roomChannel{
			{Channel: lounge, Tasks: 2, Current: true},
			{Channel: bot.Channel{ID: "21", Name: "Raid voice", Voice: true, Usable: true}, Tasks: 1},
			{Channel: bot.Channel{ID: "22", Name: "AFK", Voice: true, Usable: true}},
		},
		TaskCount: 3, Members: true, MGroups: groups, MHidden: hidden, MMore: more, MPage: 1, Presence: true}

	req := httptest.NewRequest("GET", d.Self, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, sess))
	rec := httptest.NewRecorder()
	s.execute(rec, req, http.StatusOK, "room", pageData{Session: sess, Prefs: store.DefaultPrefs, Data: d, Shell: sh})
	if err := os.WriteFile(filepath.Join(dir, "room.html"), rec.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
