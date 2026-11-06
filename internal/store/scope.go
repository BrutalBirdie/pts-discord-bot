package store

import (
	"cmp"
	"slices"
	"strings"
)

// Scope limits queries to rows of some servers. Messages and media without a
// server are shared, they are in scope only when Shared is set. The zero
// Scope matches nothing, so a forgotten scope fails closed.
type Scope struct {
	all    bool
	shared bool
	guilds []string
}

// All is the scope of Cloudron users and the bot itself.
var All = Scope{all: true}

// GuildScope matches rows of the servers, and shared rows when shared is set.
func GuildScope(shared bool, guilds ...string) Scope {
	// "" would match shared rows in SQL even without shared
	return Scope{shared: shared, guilds: slices.DeleteFunc(slices.Clone(guilds), func(g string) bool { return g == "" })}
}

// RunScope is what a task or message of guildID may use when it runs: like
// Session.ItemScope for whoever saved it last, shared items only when that
// was not a Discord user. createdBy counts when updatedBy is empty.
func RunScope(guildID, updatedBy, createdBy string) Scope {
	by := cmp.Or(updatedBy, createdBy)
	if strings.HasPrefix(by, DiscordSubject("")) {
		return GuildScope(false, guildID)
	}
	return GuildScope(true, guildID)
}

// Has tells whether a row owned by guildID is in scope, "" being shared.
func (s Scope) Has(guildID string) bool {
	if s.all {
		return true
	}
	if guildID == "" {
		return s.shared
	}
	return slices.Contains(s.guilds, guildID)
}

// param lists the owners in scope, "" for shared rows. nil matches every row.
func (s Scope) param() []string {
	if s.all {
		return nil
	}
	out := append([]string{}, s.guilds...)
	if s.shared {
		out = append(out, "")
	}
	return out
}

// inScope is the SQL condition for a scope passed as parameter $n.
func inScope(col, n string) string {
	return `($` + n + `::text[] IS NULL OR coalesce(` + col + `, '') = ANY ($` + n + `))`
}
