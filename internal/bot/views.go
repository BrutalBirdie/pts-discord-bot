package bot

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	disgobot "github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// Guild, Channel and Member are what the web UI shows, read from the gateway caches.
type Guild struct {
	ID          string
	Name        string
	MemberCount int
	Cached      int // members in the cache
	Voice       int
	Text        int
	BotVoice    string // name of the voice channel the bot is in
	Playing     *Playing
	Problems    int      // channels the bot cannot use
	Missing     []string // server permissions of the invite link the bot lacks
	Icon        string   // URL on Discord's CDN, empty without an icon
	Self        Member   // the bot in this server, with its nickname and avatar
}

// invitePermissionNames name InvitePermissions as Discord's settings do.
var invitePermissionNames = []struct {
	p    discord.Permissions
	name string
}{
	{discord.PermissionViewChannel, "View Channels"}, {discord.PermissionSendMessages, "Send Messages"},
	{discord.PermissionEmbedLinks, "Embed Links"}, {discord.PermissionAttachFiles, "Attach Files"},
	{discord.PermissionConnect, "Connect"}, {discord.PermissionSpeak, "Speak"},
	{discord.PermissionChangeNickname, "Change Nickname"},
}

// MissingNick tells whether the bot cannot change its nickname in the server.
func (g Guild) MissingNick() bool { return slices.Contains(g.Missing, "Change Nickname") }

type Channel struct {
	ID       string
	Name     string
	Voice    bool
	Category string
	Usable   bool
	Problem  string
	InVoice  []string // member names in a voice channel
	People   []Member // members in a voice channel, without the bot
	HasBot   bool
	position [2]int
}

type Member struct {
	ID       string
	Name     string // nickname or display name
	Username string
	Bot      bool
	Roles    []string
	Voice    string // voice channel name
	VoiceID  string
	Avatar   string // URL on Discord's CDN
}

func initials(s string) string {
	var out []rune
	for _, f := range strings.Fields(s) {
		out = append(out, []rune(strings.ToUpper(f))[0])
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

func (g Guild) Initials() string { return initials(g.Name) }
func (m Member) Initials() string {
	return initials(m.Name)
}

func ParseID(s string) (snowflake.ID, bool) {
	id, err := snowflake.Parse(s)
	return id, err == nil && id != 0
}

func (b *Bot) Guilds() []Guild {
	c, err := b.get()
	if err != nil {
		return nil
	}
	var out []Guild
	for g := range c.Caches.Guilds() {
		out = append(out, b.guildView(c, g))
	}
	slices.SortFunc(out, func(a, b Guild) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

func (b *Bot) Guild(id string) (Guild, bool) {
	c, err := b.get()
	gid, ok := ParseID(id)
	if err != nil || !ok {
		return Guild{}, false
	}
	g, ok := c.Caches.Guild(gid)
	if !ok {
		return Guild{}, false
	}
	return b.guildView(c, g), true
}

// GuildName falls back to the ID for guilds the bot left.
func (b *Bot) GuildName(id string) string {
	if g, ok := b.Guild(id); ok {
		return g.Name
	}
	return id
}

func (b *Bot) guildView(c *disgobot.Client, g discord.CacheGuild) Guild {
	v := Guild{ID: g.ID.String(), Name: g.Name, MemberCount: g.MemberCount, Cached: c.Caches.MembersLen(g.ID)}
	for _, ch := range b.Channels(g.ID.String()) {
		if ch.Voice {
			v.Voice++
		} else {
			v.Text++
		}
		if !ch.Usable {
			v.Problems++
		}
		if ch.HasBot {
			v.BotVoice = ch.Name
		}
	}
	v.Playing = b.playingIn(g.ID)
	if u := g.IconURL(discord.WithSize(96)); u != nil {
		v.Icon = *u
	}
	if self, ok := c.Caches.SelfMember(g.ID); ok {
		v.Self = memberView(self)
		perms := c.Caches.MemberPermissions(self)
		for _, n := range invitePermissionNames {
			if !perms.Has(n.p) {
				v.Missing = append(v.Missing, n.name)
			}
		}
	}
	return v
}

// Channels returns the text and voice channels of a guild in Discord's order,
// with what the bot may do in them.
func (b *Bot) Channels(guildID string) []Channel {
	c, err := b.get()
	gid, ok := ParseID(guildID)
	if err != nil || !ok {
		return nil
	}
	self, haveSelf := c.Caches.SelfMember(gid)
	selfID := c.ID()
	categories := map[snowflake.ID]discord.GuildChannel{}
	var list []discord.GuildChannel
	for ch := range c.Caches.ChannelsForGuild(gid) {
		switch ch.Type() {
		case discord.ChannelTypeGuildCategory:
			categories[ch.ID()] = ch
		case discord.ChannelTypeGuildText, discord.ChannelTypeGuildNews, discord.ChannelTypeGuildVoice, discord.ChannelTypeGuildStageVoice:
			list = append(list, ch)
		}
	}
	inVoice := map[snowflake.ID][]Member{}
	for vs := range c.Caches.VoiceStates(gid) {
		if vs.ChannelID == nil {
			continue
		}
		m := Member{ID: vs.UserID.String(), Name: vs.UserID.String()}
		if cm, ok := c.Caches.Member(gid, vs.UserID); ok {
			m = memberView(cm)
		}
		if vs.UserID == selfID {
			m.ID = ""
		}
		inVoice[*vs.ChannelID] = append(inVoice[*vs.ChannelID], m)
	}

	out := make([]Channel, 0, len(list))
	for _, ch := range list {
		voice := ch.Type() == discord.ChannelTypeGuildVoice || ch.Type() == discord.ChannelTypeGuildStageVoice
		v := Channel{ID: ch.ID().String(), Name: ch.Name(), Voice: voice, Usable: true, position: [2]int{-1, ch.Position()}}
		if voice {
			// Discord lists voice channels after text channels within a category
			v.position[1] += 1 << 20
		}
		if p := ch.ParentID(); p != nil {
			if cat, ok := categories[*p]; ok {
				v.Category = cat.Name()
				v.position[0] = cat.Position()
			}
		}
		for _, m := range inVoice[ch.ID()] {
			if m.ID == "" {
				v.HasBot = true
			} else {
				v.InVoice = append(v.InVoice, m.Name)
				v.People = append(v.People, m)
			}
		}
		slices.SortFunc(v.People, func(a, b Member) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		if haveSelf {
			perms := c.Caches.MemberPermissionsInChannel(ch, self)
			need := discord.PermissionViewChannel | discord.PermissionSendMessages
			what := "View Channel and Send Messages"
			if voice {
				need, what = discord.PermissionViewChannel|discord.PermissionConnect|discord.PermissionSpeak, "View Channel, Connect and Speak"
			}
			if !perms.Has(need) {
				v.Usable, v.Problem = false, "The bot needs "+what+" here."
			}
		}
		out = append(out, v)
	}
	slices.SortStableFunc(out, func(a, b Channel) int {
		if c := cmp.Compare(a.position[0], b.position[0]); c != 0 {
			return c
		}
		return cmp.Compare(a.position[1], b.position[1])
	})
	return out
}

// Channel finds a channel of any guild.
func (b *Bot) Channel(id string) (Channel, string, bool) {
	c, err := b.get()
	cid, ok := ParseID(id)
	if err != nil || !ok {
		return Channel{}, "", false
	}
	ch, ok := c.Caches.Channel(cid)
	if !ok {
		return Channel{}, "", false
	}
	for _, v := range b.Channels(ch.GuildID().String()) {
		if v.ID == id {
			return v, ch.GuildID().String(), true
		}
	}
	return Channel{}, "", false
}

// ChannelName returns "#name" or the ID when the channel is unknown.
func (b *Bot) ChannelName(id string) string {
	if ch, _, ok := b.Channel(id); ok {
		return ch.Name
	}
	return id
}

func (b *Bot) Members(guildID string) []Member {
	c, err := b.get()
	gid, ok := ParseID(guildID)
	if err != nil || !ok {
		return nil
	}
	roles := map[snowflake.ID]string{}
	for r := range c.Caches.Roles(gid) {
		roles[r.ID] = r.Name
	}
	var out []Member
	for m := range c.Caches.Members(gid) {
		v := memberView(m)
		for _, r := range m.RoleIDs {
			if n, ok := roles[r]; ok {
				v.Roles = append(v.Roles, n)
			}
		}
		if vs, ok := c.Caches.VoiceState(gid, m.User.ID); ok && vs.ChannelID != nil {
			if ch, ok := c.Caches.Channel(*vs.ChannelID); ok {
				v.Voice, v.VoiceID = ch.Name(), ch.ID().String()
			}
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Member) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

func memberView(m discord.Member) Member {
	return Member{ID: m.User.ID.String(), Name: m.EffectiveName(), Username: m.User.Username, Bot: m.User.Bot, Avatar: m.EffectiveAvatarURL(discord.WithSize(64))}
}

// MemberName returns the display name of a user in a guild, or the ID.
func (b *Bot) MemberName(guildID, userID string) string {
	c, err := b.get()
	gid, ok1 := ParseID(guildID)
	uid, ok2 := ParseID(userID)
	if err != nil || !ok1 || !ok2 {
		return userID
	}
	if m, ok := c.Caches.Member(gid, uid); ok {
		return m.EffectiveName()
	}
	return userID
}

// RefreshMembers asks the gateway for all members of every guild.
func (b *Bot) RefreshMembers(ctx context.Context) error {
	c, err := b.get()
	if err != nil {
		return err
	}
	if !b.Status().MembersIntent {
		return nil
	}
	for g := range c.Caches.Guilds() {
		rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, err := c.MemberChunkingManager.RequestAllMembers(rctx, g.ID)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
