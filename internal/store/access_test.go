package store

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// testStore needs an empty database, test/e2e/run.sh provides one.
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.CreateSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `TRUNCATE discord_users, access_requests, discord_grants, sessions, oidc_logins, audit_log`); err != nil {
		t.Fatal(err)
	}
	return s
}

func sessionsOf(t *testing.T, s *Store, userID string) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE username = $1`, DiscordSubject(userID)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLoginStateIsBoundToProvider(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.SaveOIDCLogin(ctx, ProviderDiscord, "st", "ver", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TakeOIDCLogin(ctx, ProviderCloudron, "st"); !errors.Is(err, ErrNotFound) {
		t.Errorf("discord state accepted by the cloudron callback: %v", err)
	}
	if v, _, err := s.TakeOIDCLogin(ctx, ProviderDiscord, "st"); err != nil || v != "ver" {
		t.Errorf("take: %q %v", v, err)
	}
	if _, _, err := s.TakeOIDCLogin(ctx, ProviderDiscord, "st"); !errors.Is(err, ErrNotFound) {
		t.Errorf("state used twice: %v", err)
	}
}

func TestAccessLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const mira, lee = "100", "200"
	for _, u := range []DiscordUser{{ID: mira, Username: "mira.k", GlobalName: "Mira"}, {ID: lee, Username: "lee.w"}} {
		if blocked, err := s.UpsertDiscordUser(ctx, u); err != nil || blocked {
			t.Fatalf("upsert: %v %v", blocked, err)
		}
	}
	login := func(id string) {
		if err := s.CreateSession(ctx, "tok-"+id+time.Now().String(), Session{Username: DiscordSubject(id), DisplayName: id, Provider: ProviderDiscord, GuildIDs: []string{"1", "2"}, CSRFToken: "c"}); err != nil {
			t.Fatal(err)
		}
	}
	login(mira)
	if _, err := s.CreateAccessRequest(ctx, mira, []string{"1", "2"}, "raids"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAccessRequest(ctx, mira, []string{"1"}, ""); !errors.Is(err, ErrPending) {
		t.Errorf("second pending request: %v", err)
	}
	if err := s.ApproveAccess(ctx, mira, []string{"3"}, "ann"); !errors.Is(err, ErrNotFound) {
		t.Errorf("approve a server that was not asked for: %v", err)
	}
	if err := s.ApproveAccess(ctx, mira, []string{"2"}, "ann"); err != nil {
		t.Fatal(err)
	}
	acc, err := s.DiscordAccess(ctx, mira)
	if err != nil || !slices.Equal(acc.Grants, []string{"2"}) || acc.Latest.Status != AccessApproved || acc.Latest.DecidedBy != "ann" {
		t.Fatalf("after approve: %+v %v", acc, err)
	}
	if err := s.ApproveAccess(ctx, mira, []string{"2"}, "ann"); !errors.Is(err, ErrNotFound) {
		t.Errorf("approve twice: %v", err)
	}
	if sessionsOf(t, s, mira) != 1 {
		t.Error("approve must keep the session")
	}

	if _, err := s.CreateAccessRequest(ctx, lee, []string{"1"}, ""); err != nil {
		t.Fatal(err)
	}
	counts, err := s.AccessCounts(ctx)
	if err != nil || counts[AccessPending] != 1 || counts[AccessApproved] != 1 {
		t.Errorf("counts: %v %v", counts, err)
	}
	list, total, err := s.ListAccess(ctx, AccessQuery{State: AccessApproved, Search: "MIR", Limit: 25})
	if err != nil || total != 1 || len(list) != 1 || list[0].User.ID != mira || list[0].GrantedBy != "ann" || list[0].Request.Note != "raids" {
		t.Errorf("list approved: %+v %d %v", list, total, err)
	}
	if list, _, _ := s.ListAccess(ctx, AccessQuery{State: AccessPending, Search: "Gaming", GuildIDs: []string{"1"}, Limit: 25}); len(list) != 1 || list[0].User.ID != lee {
		t.Errorf("search by server: %+v", list)
	}
	if list, _, _ := s.ListAccess(ctx, AccessQuery{State: AccessPending, Search: "%", Limit: 25}); len(list) != 0 {
		t.Errorf("wildcards must be escaped: %+v", list)
	}

	if err := s.RevokeAccess(ctx, mira, "ann"); err != nil {
		t.Fatal(err)
	}
	acc, _ = s.DiscordAccess(ctx, mira)
	if len(acc.Grants) != 0 || acc.Latest.Status != AccessRevoked || sessionsOf(t, s, mira) != 0 {
		t.Errorf("after revoke: %+v sessions=%d", acc, sessionsOf(t, s, mira))
	}

	login(lee)
	if err := s.DenyAccess(ctx, lee, "ann"); err != nil || sessionsOf(t, s, lee) != 0 {
		t.Errorf("deny: %v, sessions=%d", err, sessionsOf(t, s, lee))
	}
	if _, err := s.CreateAccessRequest(ctx, lee, []string{"1"}, ""); err != nil {
		t.Errorf("ask again after deny: %v", err)
	}
	login(lee)
	if err := s.BlockDiscordUser(ctx, lee, "ann"); err != nil {
		t.Fatal(err)
	}
	acc, _ = s.DiscordAccess(ctx, lee)
	if !acc.User.Blocked || acc.Latest.Status != AccessDenied || sessionsOf(t, s, lee) != 0 {
		t.Errorf("after block: %+v", acc)
	}
	if _, err := s.CreateAccessRequest(ctx, lee, []string{"1"}, ""); !errors.Is(err, ErrBlocked) {
		t.Errorf("blocked user asked: %v", err)
	}
	if blocked, _ := s.UpsertDiscordUser(ctx, DiscordUser{ID: lee, Username: "lee.w"}); !blocked {
		t.Error("login of a blocked user not flagged")
	}
	if counts, _ := s.AccessCounts(ctx); counts[AccessBlocked] != 1 || counts[AccessDenied] != 1 || counts[AccessPending] != 0 {
		t.Errorf("counts after block: %v", counts)
	}
	if err := s.UnblockDiscordUser(ctx, lee); err != nil {
		t.Fatal(err)
	}
	if err := s.UnblockDiscordUser(ctx, lee); !errors.Is(err, ErrNotFound) {
		t.Errorf("unblock twice: %v", err)
	}
}

func TestAutoDenyAndGrantRemoval(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"1", "2", "3"} {
		if _, err := s.UpsertDiscordUser(ctx, DiscordUser{ID: id, Username: "u" + id}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateAccessRequest(ctx, id, []string{"10", "11"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE access_requests SET expires_at = now() - interval '1 minute' WHERE user_id = '1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.ApproveAccess(ctx, "3", []string{"10", "11"}, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE access_requests SET expires_at = now() - interval '1 minute' WHERE user_id = '3'`); err != nil {
		t.Fatal(err)
	}
	denied, err := s.AutoDenyExpired(ctx)
	if err != nil || len(denied) != 1 || denied[0].UserID != "1" || denied[0].DecidedBy != SystemActor || denied[0].Status != AccessDenied {
		t.Fatalf("auto deny: %+v %v", denied, err)
	}
	if again, _ := s.AutoDenyExpired(ctx); len(again) != 0 {
		t.Errorf("auto deny twice: %+v", again)
	}
	if n, _ := s.PendingAccessCount(ctx); n != 1 {
		t.Errorf("pending: %d", n)
	}

	if err := s.CreateSession(ctx, "t3", Session{Username: DiscordSubject("3"), Provider: ProviderDiscord, CSRFToken: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveGrant(ctx, "3", "10"); err != nil || sessionsOf(t, s, "3") != 1 {
		t.Errorf("one grant left, session must stay: %v", err)
	}
	users, err := s.RemoveGuildGrants(ctx, "11")
	if err != nil || !slices.Equal(users, []string{"3"}) || sessionsOf(t, s, "3") != 0 {
		t.Errorf("bot left: %v %v sessions=%d", users, err, sessionsOf(t, s, "3"))
	}
	if grants, _ := s.AllGrants(ctx); len(grants) != 0 {
		t.Errorf("grants left: %v", grants)
	}
}
