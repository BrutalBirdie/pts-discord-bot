package store

import (
	"strings"
	"testing"
)

func TestMediaUsedByProfileSteps(t *testing.T) {
	for _, want := range []string{`jsonb_build_object('avatar_media_id', m.id)`, `jsonb_build_object('media_id', m.id)`, `jsonb_build_object('media_ids', jsonb_build_array(m.id))`} {
		if !strings.Contains(listMediaQuery, want) {
			t.Errorf("usage query misses %s", want)
		}
	}
	if !strings.Contains(schema, "CREATE TABLE IF NOT EXISTS bot_profile_changes") {
		t.Error("schema misses bot_profile_changes")
	}
}
