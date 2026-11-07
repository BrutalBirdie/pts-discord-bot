package store

import (
	"slices"
)

// Scope limits queries of tasks and runs to some servers. The zero Scope
// matches nothing, so a forgotten scope fails closed. Media and messages are
// open to every user and have no scope.
type Scope struct {
	all    bool
	guilds []string
}

// All is the scope of Cloudron users and the bot itself.
var All = Scope{all: true}

// GuildScope matches rows of the servers.
func GuildScope(guilds ...string) Scope {
	return Scope{guilds: slices.DeleteFunc(slices.Clone(guilds), func(g string) bool { return g == "" })}
}

// Has tells whether a row of guildID is in scope.
func (s Scope) Has(guildID string) bool {
	if s.all {
		return true
	}
	return guildID != "" && slices.Contains(s.guilds, guildID)
}

// param lists the servers in scope, nil matches every row.
func (s Scope) param() []string {
	if s.all {
		return nil
	}
	return append([]string{}, s.guilds...)
}

// inScope is the SQL condition for a scope passed as parameter $n.
func inScope(col, n string) string {
	return `($` + n + `::text[] IS NULL OR coalesce(` + col + `, '') = ANY ($` + n + `))`
}

// offered is the SQL condition for the media or messages of alias offered to
// the server passed as parameter $n: all but those another server keeps to
// itself. An empty server offers everything.
func offered(alias, n string) string {
	return `($` + n + ` = '' OR NOT ` + alias + `.group_only OR ` + alias + `.guild_id = $` + n + `)`
}

// ownFirst orders the items of the server passed as parameter $n first.
func ownFirst(alias, n string) string {
	return `($` + n + ` <> '' AND ` + alias + `.guild_id IS DISTINCT FROM $` + n + `)`
}

// inGroup is the SQL condition for the group filter passed as parameter $n:
// "" for all, NoGroup for items without a server, else a server ID.
func inGroup(alias, n string) string {
	return `($` + n + ` = '' OR coalesce(` + alias + `.guild_id, '` + NoGroup + `') = $` + n + `)`
}

// NoGroup filters lists to the media and messages without a server.
const NoGroup = "none"
