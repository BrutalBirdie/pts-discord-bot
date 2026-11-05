// Package access decides who is an admin of the panel.
package access

import (
	"slices"
	"strings"
)

// Admins are Cloudron usernames from BOT_ADMINS and group names from BOT_ADMIN_GROUP.
type Admins struct {
	Users  []string
	Groups []string
}

func Parse(users, groups string) Admins {
	return Admins{Users: splitList(users), Groups: splitList(groups)}
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func (a Admins) IsAdmin(username string, groups []string) bool {
	if username == "" {
		return false
	}
	if slices.Contains(a.Users, username) {
		return true
	}
	for _, g := range groups {
		if slices.Contains(a.Groups, g) {
			return true
		}
	}
	return false
}
