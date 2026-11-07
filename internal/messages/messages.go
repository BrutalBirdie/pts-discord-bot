// Package messages holds saved Discord messages and checks them against Discord's limits.
package messages

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits from https://docs.discord.com/developers/resources/message
const (
	MaxContent      = 2000
	MaxEmbeds       = 10
	MaxEmbedTotal   = 6000
	MaxTitle        = 256
	MaxDescription  = 4096
	MaxFields       = 25
	MaxFieldName    = 256
	MaxFieldValue   = 1024
	MaxFooter       = 2048
	MaxFiles        = 10
	MaxRequestBytes = 25 << 20
	MaxFileBytes    = 20 << 20
)

// Button limits from https://docs.discord.com/developers/components/reference#button
const (
	MaxButtons     = 25
	ButtonsPerRow  = 5
	MaxButtonLabel = 80
	MaxButtonReply = 300
	MaxButtonURL   = 512
)

// Button styles; a link button is always grey.
const (
	StylePrimary   = "primary"
	StyleSecondary = "secondary"
	StyleSuccess   = "success"
	StyleDanger    = "danger"
)

var ButtonStyles = []string{StylePrimary, StyleSecondary, StyleSuccess, StyleDanger}

func StyleLabel(s string) string {
	return map[string]string{StylePrimary: "Blurple", StyleSecondary: "Grey", StyleSuccess: "Green", StyleDanger: "Red"}[s]
}

// Button starts a task for the member who clicks it, or opens a link.
type Button struct {
	Label  string `json:"label,omitempty"`
	Emoji  string `json:"emoji,omitempty"`
	Style  string `json:"style,omitempty"`
	TaskID int64  `json:"task_id,omitempty"`
	URL    string `json:"url,omitempty"`
	Reply  string `json:"reply,omitempty"` // shown only to the member who clicked, "Started <task>." when empty
}

func (b Button) IsLink() bool { return b.TaskID == 0 }

type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type Embed struct {
	Title        string  `json:"title,omitempty"`
	URL          string  `json:"url,omitempty"`
	Description  string  `json:"description,omitempty"`
	Color        string  `json:"color,omitempty"` // #rrggbb
	ImageMediaID int64   `json:"image_media_id,omitempty"`
	ThumbMediaID int64   `json:"thumb_media_id,omitempty"`
	Fields       []Field `json:"fields,omitempty"`
	Footer       string  `json:"footer,omitempty"`
}

type Message struct {
	ID        int64
	Name      string
	Content   string
	Embeds    []Embed
	Files     []int64 // media ids sent as attachments
	Buttons   []Button
	GuildID   string // server group, "" for none
	GroupOnly bool   // not suggested to other servers than GuildID
	UpdatedBy string
	UpdatedAt time.Time
}

func runes(s string) int { return utf8.RuneCountInString(s) }

// EmbedChars counts what Discord counts against the 6000 character limit.
func (m Message) EmbedChars() int {
	n := 0
	for _, e := range m.Embeds {
		n += runes(e.Title) + runes(e.Description) + runes(e.Footer)
		for _, f := range e.Fields {
			n += runes(f.Name) + runes(f.Value)
		}
	}
	return n
}

func (m Message) ContentChars() int { return runes(m.Content) }

func (e Embed) ColorInt() int {
	n, err := strconv.ParseInt(strings.TrimPrefix(e.Color, "#"), 16, 32)
	if err != nil {
		return 0
	}
	return int(n)
}

func (e Embed) Empty() bool {
	return e.Title == "" && e.Description == "" && e.ImageMediaID == 0 && e.ThumbMediaID == 0 && len(e.Fields) == 0 && e.Footer == ""
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Problems lists everything Discord would reject. File sizes are checked when sending.
func (m Message) Problems() []string {
	var out []string
	if strings.TrimSpace(m.Name) == "" {
		out = append(out, "Give the message a name.")
	}
	if m.Content == "" && len(m.Embeds) == 0 && len(m.Files) == 0 && len(m.Buttons) == 0 {
		out = append(out, "Write a text, add an embed or a file.")
	}
	if n := m.ContentChars(); n > MaxContent {
		out = append(out, fmt.Sprintf("The text has %d characters, Discord allows %d.", n, MaxContent))
	}
	if len(m.Embeds) > MaxEmbeds {
		out = append(out, fmt.Sprintf("At most %d embeds.", MaxEmbeds))
	}
	if n := m.EmbedChars(); n > MaxEmbedTotal {
		out = append(out, fmt.Sprintf("The embeds have %d characters together, Discord allows %d.", n, MaxEmbedTotal))
	}
	if len(m.Files) > MaxFiles {
		out = append(out, fmt.Sprintf("At most %d files.", MaxFiles))
	}
	if len(m.Buttons) > MaxButtons {
		out = append(out, fmt.Sprintf("At most %d buttons.", MaxButtons))
	}
	for i, b := range m.Buttons {
		p := fmt.Sprintf("Button %d: ", i+1)
		if strings.TrimSpace(b.Label) == "" && b.Emoji == "" {
			out = append(out, p+"give it a label or an emoji.")
		}
		if runes(b.Label) > MaxButtonLabel {
			out = append(out, p+fmt.Sprintf("the label is longer than %d characters.", MaxButtonLabel))
		}
		if runes(b.Emoji) > 10 || strings.ContainsAny(b.Emoji, " <>") {
			out = append(out, p+"use one emoji.")
		}
		if b.IsLink() {
			if !strings.HasPrefix(b.URL, "https://") && !strings.HasPrefix(b.URL, "http://") || len(b.URL) > MaxButtonURL {
				out = append(out, p+"pick a task or enter a link that starts with https://.")
			}
		} else if b.Style != "" && !slices.Contains(ButtonStyles, b.Style) {
			out = append(out, p+fmt.Sprintf("unknown style %q.", b.Style))
		}
		if runes(b.Reply) > MaxButtonReply {
			out = append(out, p+fmt.Sprintf("the reply is longer than %d characters.", MaxButtonReply))
		}
	}
	for i, e := range m.Embeds {
		p := fmt.Sprintf("Embed %d: ", i+1)
		if e.Empty() {
			out = append(out, p+"it is empty, fill it or remove it.")
		}
		if runes(e.Title) > MaxTitle {
			out = append(out, p+fmt.Sprintf("the title is longer than %d characters.", MaxTitle))
		}
		if runes(e.Description) > MaxDescription {
			out = append(out, p+fmt.Sprintf("the description is longer than %d characters.", MaxDescription))
		}
		if runes(e.Footer) > MaxFooter {
			out = append(out, p+fmt.Sprintf("the footer is longer than %d characters.", MaxFooter))
		}
		if e.Color != "" && !colorRe.MatchString(e.Color) {
			out = append(out, p+"the color must look like #4752c4.")
		}
		if e.URL != "" && !strings.HasPrefix(e.URL, "https://") && !strings.HasPrefix(e.URL, "http://") {
			out = append(out, p+"the title link must start with https://.")
		}
		if len(e.Fields) > MaxFields {
			out = append(out, p+fmt.Sprintf("at most %d fields.", MaxFields))
		}
		for j, f := range e.Fields {
			if strings.TrimSpace(f.Name) == "" || strings.TrimSpace(f.Value) == "" {
				out = append(out, p+fmt.Sprintf("field %d needs a name and a value.", j+1))
			}
			if runes(f.Name) > MaxFieldName || runes(f.Value) > MaxFieldValue {
				out = append(out, p+fmt.Sprintf("field %d is too long (name %d, value %d characters).", j+1, MaxFieldName, MaxFieldValue))
			}
		}
	}
	return out
}

// Vars fill the placeholders when a message is sent.
type Vars struct {
	Now        time.Time
	Channel    string
	Server     string
	MemberID   string // the member who started the run, for {member}
	MemberName string
}

// Expand replaces {date}, {time}, {channel} and {server}.
func Expand(s string, v Vars) string {
	if !strings.Contains(s, "{") {
		return s
	}
	return strings.NewReplacer(
		"{date}", v.Now.Format("2006-01-02"),
		"{time}", v.Now.Format("15:04"),
		"{channel}", v.Channel,
		"{server}", v.Server,
		"{member}", mention(v.MemberID, v.MemberName),
		"{member_name}", v.MemberName,
	).Replace(s)
}

var Placeholders = []string{"{date}", "{time}", "{channel}", "{server}", "{member}", "{member_name}"}

func mention(id, name string) string {
	if id == "" {
		return name
	}
	return "<@" + id + ">"
}

// MentionRe matches Discord user mentions like <@123> and <@!123>.
var MentionRe = regexp.MustCompile(`<@!?([0-9]{15,21})>`)
