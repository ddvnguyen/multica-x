package handler

// M-551: trusted-gateway auto-login. POST /auth/portal-session mints a
// session for the single allowlisted email when the portal gateway presents
// the shared secret. Reachability is the access control: the handler is
// registered ONLY on the dedicated internal listener
// (MULTICA_PORTAL_LOGIN_ADDR, 127.0.0.1-only), never on the public router.
// Disabled unless MULTICA_PORTAL_LOGIN_SECRET + MULTICA_PORTAL_LOGIN_EMAIL
// are both set; no users are ever created.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type portalSessionMockDB struct {
	mockDB
	user    db.User
	userErr error
}

func (m *portalSessionMockDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	if strings.HasPrefix(sql, "-- name: GetUserByEmail :one\n") {
		return &portalSessionUserRow{user: m.user, err: m.userErr}
	}
	return m.mockDB.QueryRow(ctx, sql, args)
}

type portalSessionUserRow struct {
	pgx.Row
	user db.User
	err  error
}

func (r *portalSessionUserRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 18 {
		return fmt.Errorf("portalSessionUserRow: got %d destinations, want 18", len(dest))
	}
	setters := []func() error{
		func() error {
			v, ok := dest[0].(*pgtype.UUID)
			if !ok {
				return fmt.Errorf("dest[0] is %T, want *pgtype.UUID", dest[0])
			}
			*v = r.user.ID
			return nil
		},
		func() error {
			v, ok := dest[1].(*string)
			if !ok {
				return fmt.Errorf("dest[1] is %T, want *string", dest[1])
			}
			*v = r.user.Name
			return nil
		},
		func() error {
			v, ok := dest[2].(*string)
			if !ok {
				return fmt.Errorf("dest[2] is %T, want *string", dest[2])
			}
			*v = r.user.Email
			return nil
		},
		func() error {
			v, ok := dest[3].(*pgtype.Text)
			if !ok {
				return fmt.Errorf("dest[3] is %T, want *pgtype.Text", dest[3])
			}
			*v = r.user.AvatarUrl
			return nil
		},
		func() error {
			v, ok := dest[4].(*pgtype.Timestamptz)
			if !ok {
				return fmt.Errorf("dest[4] is %T, want *pgtype.Timestamptz", dest[4])
			}
			*v = r.user.CreatedAt
			return nil
		},
		func() error {
			v, ok := dest[5].(*pgtype.Timestamptz)
			if !ok {
				return fmt.Errorf("dest[5] is %T, want *pgtype.Timestamptz", dest[5])
			}
			*v = r.user.UpdatedAt
			return nil
		},
		func() error {
			v, ok := dest[6].(*pgtype.Timestamptz)
			if !ok {
				return fmt.Errorf("dest[6] is %T, want *pgtype.Timestamptz", dest[6])
			}
			*v = r.user.OnboardedAt
			return nil
		},
		func() error {
			v, ok := dest[7].(*[]byte)
			if !ok {
				return fmt.Errorf("dest[7] is %T, want *[]byte", dest[7])
			}
			*v = r.user.OnboardingQuestionnaire
			return nil
		},
		func() error {
			v, ok := dest[8].(*pgtype.Text)
			if !ok {
				return fmt.Errorf("dest[8] is %T, want *pgtype.Text", dest[8])
			}
			*v = r.user.CloudWaitlistEmail
			return nil
		},
		func() error {
			v, ok := dest[9].(*pgtype.Text)
			if !ok {
				return fmt.Errorf("dest[9] is %T, want *pgtype.Text", dest[9])
			}
			*v = r.user.CloudWaitlistReason
			return nil
		},
		func() error {
			v, ok := dest[10].(*pgtype.Text)
			if !ok {
				return fmt.Errorf("dest[10] is %T, want *pgtype.Text", dest[10])
			}
			*v = r.user.StarterContentState
			return nil
		},
		func() error {
			v, ok := dest[11].(*pgtype.Text)
			if !ok {
				return fmt.Errorf("dest[11] is %T, want *pgtype.Text", dest[11])
			}
			*v = r.user.Language
			return nil
		},
		func() error {
			v, ok := dest[12].(*string)
			if !ok {
				return fmt.Errorf("dest[12] is %T, want *string", dest[12])
			}
			*v = r.user.ProfileDescription
			return nil
		},
		func() error {
			v, ok := dest[13].(*pgtype.Text)
			if !ok {
				return fmt.Errorf("dest[13] is %T, want *pgtype.Text", dest[13])
			}
			*v = r.user.Timezone
			return nil
		},
		func() error {
			v, ok := dest[14].(*pgtype.Text)
			if !ok {
				return fmt.Errorf("dest[14] is %T, want *pgtype.Text", dest[14])
			}
			*v = r.user.WhatsNewSeenVersion
			return nil
		},
		func() error {
			v, ok := dest[15].(*int32)
			if !ok {
				return fmt.Errorf("dest[15] is %T, want *int32", dest[15])
			}
			*v = r.user.StarPromptShownCount
			return nil
		},
		func() error {
			v, ok := dest[16].(*pgtype.Timestamptz)
			if !ok {
				return fmt.Errorf("dest[16] is %T, want *pgtype.Timestamptz", dest[16])
			}
			*v = r.user.StarPromptLastShownAt
			return nil
		},
		func() error {
			v, ok := dest[17].(*pgtype.Timestamptz)
			if !ok {
				return fmt.Errorf("dest[17] is %T, want *pgtype.Timestamptz", dest[17])
			}
			*v = r.user.StarPromptStarredAt
			return nil
		},
	}
	for _, set := range setters {
		if err := set(); err != nil {
			return err
		}
	}
	return nil
}

func portalTestUser(idByte byte) db.User {
	return db.User{
		ID:                      pgtype.UUID{Bytes: [16]byte{idByte, 0, 0, 0, 0, 0, 0x40, 0, 0x80, 0, 0, 0, 0, 0, 0, 0}, Valid: true},
		Name:                    "Portal Owner",
		Email:                   "ddvnguyen@gmail.com",
		OnboardingQuestionnaire: []byte("{}"),
		ProfileDescription:      "",
	}
}

func portalSessionRequest(secret string) *http.Request {
	req := testutil.JSONRequest(http.MethodPost, "/auth/portal-session", map[string]string{})
	if secret != "" {
		req.Header.Set("X-Portal-Login-Secret", secret)
	}
	return req
}

func portalAuthCookieValue(resp *testutil.Response, name string) string {
	for _, c := range resp.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func TestPortalSession(t *testing.T) {
	const secret = "test-portal-secret-0123456789abcdef"
	const email = "ddvnguyen@gmail.com"

	withEnv := func(t *testing.T, secret, email string) {
		t.Setenv("MULTICA_PORTAL_LOGIN_SECRET", secret)
		t.Setenv("MULTICA_PORTAL_LOGIN_EMAIL", email)
	}

	t.Run("disabled_without_env", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{user: portalTestUser(1)})
		withEnv(t, "", "")
		req := portalSessionRequest(secret)
		testutil.Call(t, h.PortalSession, req).Want(http.StatusNotFound)
	})

	t.Run("wrong_secret", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{user: portalTestUser(2)})
		withEnv(t, secret, email)
		testutil.Call(t, h.PortalSession, portalSessionRequest("wrong-secret")).Want(http.StatusNotFound)
		resp := testutil.Call(t, h.PortalSession, portalSessionRequest("wrong-secret"))
		if got := portalAuthCookieValue(resp, auth.AuthCookieName); got != "" {
			t.Fatalf("wrong secret must not set auth cookie, got %q", got)
		}
	})

	t.Run("missing_secret_header", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{user: portalTestUser(3)})
		withEnv(t, secret, email)
		testutil.Call(t, h.PortalSession, portalSessionRequest("")).Want(http.StatusNotFound)
	})

	t.Run("unknown_email", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{userErr: pgx.ErrNoRows})
		withEnv(t, secret, email)
		testutil.Call(t, h.PortalSession, portalSessionRequest(secret)).Want(http.StatusNotFound)
	})

	t.Run("happy_path", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{user: portalTestUser(5)})
		withEnv(t, secret, email)
		resp := testutil.Call(t, h.PortalSession, portalSessionRequest(secret)).Want(http.StatusOK)
		var body LoginResponse
		resp.JSON(&body)
		if body.User.Email != email {
			t.Fatalf("user email = %q, want %q", body.User.Email, email)
		}
		if body.Token == "" {
			t.Fatal("expected a token in the response body")
		}
		cookieVal := portalAuthCookieValue(resp, auth.AuthCookieName)
		if cookieVal == "" {
			t.Fatal("expected multica_auth cookie to be set")
		}
		if cookieVal != body.Token {
			t.Fatal("auth cookie must carry the same token as the body")
		}
		claims := jwt.MapClaims{}
		if _, err := jwt.ParseWithClaims(body.Token, claims, func(*jwt.Token) (any, error) {
			return auth.JWTSecret(), nil
		}); err != nil {
			t.Fatalf("token does not verify: %v", err)
		}
		if claims["email"] != email {
			t.Fatalf("token email = %v, want %s", claims["email"], email)
		}
	})

	t.Run("no_peer_check_any_remote_accepted", func(t *testing.T) {
		// Reachability (internal listener) is the control; the handler
		// itself must not reject any peer, loopback or not.
		for _, remote := range []string{"127.0.0.1:5000", "[::1]:5000", "10.89.1.4:5000", "203.0.113.7:5000"} {
			h := newTestHandler(Config{})
			h.Queries = db.New(&portalSessionMockDB{user: portalTestUser(6)})
			withEnv(t, secret, email)
			req := portalSessionRequest(secret)
			req.RemoteAddr = remote
			testutil.Call(t, h.PortalSession, req).Want(http.StatusOK)
		}
	})

	t.Run("database_error", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{userErr: errors.New("connection refused")})
		withEnv(t, secret, email)
		testutil.Call(t, h.PortalSession, portalSessionRequest(secret)).Want(http.StatusInternalServerError)
	})

	t.Run("temporarily_disabled_email", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{user: portalTestUser(7)})
		// pdzzer68@embassybase.com is on the temporary emergency denylist.
		withEnv(t, secret, "pdzzer68@embassybase.com")
		testutil.Call(t, h.PortalSession, portalSessionRequest(secret)).Want(http.StatusForbidden)
	})

	t.Run("never_creates_users", func(t *testing.T) {
		h := newTestHandler(Config{})
		h.Queries = db.New(&portalSessionMockDB{userErr: pgx.ErrNoRows})
		withEnv(t, secret, "nobody-new@example.com")
		resp := testutil.Call(t, h.PortalSession, portalSessionRequest(secret))
		if resp.Code != http.StatusNotFound {
			t.Fatalf("unknown email must 404 (never create), got %d: %s", resp.Code, resp.Body.String())
		}
	})
}
