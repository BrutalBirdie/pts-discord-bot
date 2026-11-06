package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type StepType string

const (
	Join    StepType = "join"
	Play    StepType = "play"
	Leave   StepType = "leave"
	Message StepType = "message"
	DM      StepType = "dm"
	Wait    StepType = "wait"
	Profile StepType = "profile"
)

var StepTypes = []StepType{Join, Play, Message, DM, Wait, Profile, Leave}

const (
	MaxSteps  = 30
	MaxVolume = 200
	MaxSounds = 25
)

// Order of the sounds of a play step.
const (
	OrderSequence = "sequence"
	OrderRandom   = "random"
)

type Step struct {
	Type          StepType `json:"type"`
	ChannelID     string   `json:"channel_id,omitempty"`      // join, message, wait
	FollowUserID  string   `json:"follow_user_id,omitempty"`  // join: the voice channel this member is in
	MediaID       int64    `json:"media_id,omitempty"`        // play: legacy single sound, folded into MediaIDs when loaded
	MediaIDs      []int64  `json:"media_ids,omitempty"`       // play
	Tag           string   `json:"tag,omitempty"`             // play: legacy, a random sound with this tag
	Order         string   `json:"order,omitempty"`           // play: OrderSequence or OrderRandom
	Find          string   `json:"-"`                         // play: sound name typed in the editor without JavaScript
	ToTrigger     bool     `json:"to_trigger,omitempty"`      // join, dm, wait: the member who started the run
	Volume        int      `json:"volume,omitempty"`          // play, percent
	UserID        string   `json:"user_id,omitempty"`         // dm, wait
	MessageID     int64    `json:"message_id,omitempty"`      // message, dm: a saved message
	Text          string   `json:"text,omitempty"`            // message, dm: used without MessageID
	Seconds       int      `json:"seconds,omitempty"`         // wait, before wait_ms
	WaitMS        int64    `json:"wait_ms,omitempty"`         // wait, the timeout with Until
	Until         string   `json:"until,omitempty"`           // wait: one of Untils
	OnTimeout     string   `json:"on_timeout,omitempty"`      // wait with Until
	WaitInput     string   `json:"-"`                         // wait: editor text that did not parse
	Nick          string   `json:"nick,omitempty"`            // profile, may hold message placeholders
	ResetNick     bool     `json:"reset_nick,omitempty"`      // profile
	AvatarMediaID int64    `json:"avatar_media_id,omitempty"` // profile: an image, not media_id, which play steps fold into MediaIDs
	ResetAvatar   bool     `json:"reset_avatar,omitempty"`    // profile
}

func (s Step) Validate() error {
	switch s.Type {
	case Join:
		if s.ChannelID == "" && s.FollowUserID == "" && !s.ToTrigger {
			return errors.New("pick a voice channel")
		}
	case Play:
		n := len(s.Sounds())
		if n == 0 {
			return errors.New("pick a sound")
		}
		if n > MaxSounds {
			return fmt.Errorf("at most %d sounds", MaxSounds)
		}
		if s.Order != "" && s.Order != OrderSequence && s.Order != OrderRandom {
			return fmt.Errorf("unknown order %q", s.Order)
		}
		if s.Volume < 1 || s.Volume > MaxVolume {
			return fmt.Errorf("volume must be between 1 and %d %%", MaxVolume)
		}
	case Message, DM:
		if s.Type == Message && s.ChannelID == "" {
			return errors.New("pick a text channel")
		}
		if s.Type == DM && s.UserID == "" && !s.ToTrigger {
			return errors.New("pick a member")
		}
		if s.MessageID == 0 && strings.TrimSpace(s.Text) == "" {
			return errors.New("write a text or pick a saved message")
		}
		if utf8.RuneCountInString(s.Text) > 2000 {
			return errors.New("the text is longer than 2000 characters")
		}
	case Wait:
		return s.validateWait()
	case Profile:
		return s.validateProfile()
	case Leave:
	default:
		return fmt.Errorf("unknown step %q", s.Type)
	}
	return nil
}

func (s *Step) UnmarshalJSON(b []byte) error {
	type plain Step
	if err := json.Unmarshal(b, (*plain)(s)); err != nil {
		return err
	}
	if s.MediaID != 0 {
		s.MediaIDs = append([]int64{s.MediaID}, s.MediaIDs...)
		s.MediaID = 0
	}
	return nil
}

// SoundRef is one entry of a play step: a sound, or a random sound with a legacy tag.
type SoundRef struct {
	MediaID int64
	Tag     string
}

// Sounds lists the entries of a play step, a legacy tag first.
func (s Step) Sounds() []SoundRef {
	var out []SoundRef
	if s.Tag != "" {
		out = append(out, SoundRef{Tag: s.Tag})
	}
	for _, id := range s.MediaIDs {
		out = append(out, SoundRef{MediaID: id})
	}
	return out
}

func (t StepType) Label() string {
	return map[StepType]string{Join: "Join voice channel", Play: "Play sound", Leave: "Leave voice channel", Message: "Send message", DM: "Direct message", Wait: "Wait", Profile: "Change bot profile"}[t]
}

func (t StepType) Icon() string {
	return map[StepType]string{Join: "log-in", Play: "music", Leave: "log-out", Message: "send", DM: "mail", Wait: "timer", Profile: "bot"}[t]
}

func (t StepType) Hint() string {
	return map[StepType]string{Join: "A channel, or where a member is", Play: "From the media library", Leave: "Disconnect the bot", Message: "Text, embeds, files", DM: "To one member", Wait: "e.g. 500ms, 1.5s, 5m, 2h", Profile: "Nickname and avatar in this server"}[t]
}

func (t StepType) Voice() bool { return t == Join || t == Play || t == Leave }

// Busy decides what a run does when the bot is already busy with voice in the server.
const (
	BusyWait = "wait"
	BusySkip = "skip"
)

type Task struct {
	ID           int64
	Name         string
	GuildID      string
	Enabled      bool
	Busy         string
	Slash        bool // can be started with /task in Discord
	Trigger      Trigger
	Steps        []Step
	PausedReason string // set when a run paused the task after a permanent error
	CreatedBy    string
	UpdatedBy    string
	UpdatedAt    time.Time
}

func (t Task) UsesVoice() bool {
	for _, s := range t.Steps {
		if s.Type.Voice() {
			return true
		}
	}
	return false
}

// Problems lists everything that keeps the task from being saved.
func (t Task) Problems() []string {
	var out []string
	if strings.TrimSpace(t.Name) == "" {
		out = append(out, "Give the task a name.")
	}
	if utf8.RuneCountInString(t.Name) > 100 {
		out = append(out, "The name is longer than 100 characters.")
	}
	if t.GuildID == "" {
		out = append(out, "Pick a server.")
	}
	if err := t.Trigger.Validate(); err != nil {
		out = append(out, "Schedule: "+err.Error()+".")
	}
	if len(t.Steps) == 0 {
		out = append(out, "Add at least one step.")
	}
	if len(t.Steps) > MaxSteps {
		out = append(out, fmt.Sprintf("At most %d steps.", MaxSteps))
	}
	for i, s := range t.Steps {
		if err := s.Validate(); err != nil {
			out = append(out, fmt.Sprintf("Step %d: %s.", i+1, err))
		}
	}
	return out
}
