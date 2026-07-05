package spotify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/zmb3/spotify/v2"
	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"golang.org/x/oauth2"
)

func TestAuthURLRedirectURI(t *testing.T) {
	const configured = "https://configured.example/callback"
	auth := spotifyauth.New(
		spotifyauth.WithClientID("id"),
		spotifyauth.WithRedirectURL(configured),
	)
	c := New(nil, auth)

	redirectOf := func(rawURL string) string {
		t.Helper()
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse auth url: %v", err)
		}
		return u.Query().Get("redirect_uri")
	}

	t.Run("no option uses authenticator default", func(t *testing.T) {
		if got := redirectOf(c.AuthURL("state")); got != configured {
			t.Errorf("redirect_uri = %q, want %q", got, configured)
		}
	})

	t.Run("WithRedirectURI overrides default", func(t *testing.T) {
		const override = "https://edge.example/spotify/callback"
		if got := redirectOf(c.AuthURL("state", WithRedirectURI(override))); got != override {
			t.Errorf("redirect_uri = %q, want %q", got, override)
		}
	})
}

// mockTokenStore records SaveRefreshToken and DeleteRefreshToken calls so
// Connect/Disconnect tests can assert what happened, and lets a test force a
// failure via saveErr/deleteErr.
type mockTokenStore struct {
	saveErr   error
	deleteErr error

	saveCalled  bool
	savedUser   string
	savedToken  string
	deletedUser string
}

func (m *mockTokenStore) GetRefreshToken(ctx context.Context, userID string) (string, error) {
	return "", errors.New("mockTokenStore: GetRefreshToken not expected")
}

func (m *mockTokenStore) SaveRefreshToken(ctx context.Context, userID, refreshToken string) error {
	m.saveCalled = true
	m.savedUser = userID
	m.savedToken = refreshToken
	return m.saveErr
}

func (m *mockTokenStore) DeleteRefreshToken(ctx context.Context, userID string) error {
	m.deletedUser = userID
	return m.deleteErr
}

// roundTripFunc adapts a function into an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// tokenEndpointCtx returns a context whose oauth2 HTTP client answers the token
// endpoint with the given status and JSON body, so Exchange runs without network.
func tokenEndpointCtx(status int, body string) context.Context {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}
	return context.WithValue(context.Background(), oauth2.HTTPClient, hc)
}

// okTokenBody is a successful token response granting every RequiredScope.
const okTokenBody = `{"access_token":"access","token_type":"Bearer","refresh_token":"refresh-xyz","expires_in":3600,"scope":"user-modify-playback-state user-read-playback-state playlist-read-private"}`

func newAuthForExchange() *spotifyauth.Authenticator {
	return spotifyauth.New(
		spotifyauth.WithClientID("id"),
		spotifyauth.WithClientSecret("secret"),
	)
}

func TestConnect_SavesToken(t *testing.T) {
	store := &mockTokenStore{}
	c := New(store, newAuthForExchange())

	ctx := tokenEndpointCtx(http.StatusOK, okTokenBody)
	if err := c.Connect(ctx, "user-1", "code"); err != nil {
		t.Fatalf("Connect() error = %v, want nil", err)
	}
	if !store.saveCalled {
		t.Fatal("SaveRefreshToken was not called")
	}
	if store.savedUser != "user-1" {
		t.Errorf("saved userID = %q, want %q", store.savedUser, "user-1")
	}
	if store.savedToken != "refresh-xyz" {
		t.Errorf("saved token = %q, want %q", store.savedToken, "refresh-xyz")
	}
}

func TestConnect_ExchangeError(t *testing.T) {
	store := &mockTokenStore{}
	c := New(store, newAuthForExchange())

	ctx := tokenEndpointCtx(http.StatusBadRequest, `{"error":"invalid_grant"}`)
	if err := c.Connect(ctx, "user-1", "bad-code"); err == nil {
		t.Fatal("Connect() error = nil, want non-nil")
	}
	if store.saveCalled {
		t.Error("SaveRefreshToken was called despite Exchange failure")
	}
}

func TestConnect_SaveError(t *testing.T) {
	saveErr := errors.New("db down")
	store := &mockTokenStore{saveErr: saveErr}
	c := New(store, newAuthForExchange())

	ctx := tokenEndpointCtx(http.StatusOK, okTokenBody)
	err := c.Connect(ctx, "user-1", "code")
	if !errors.Is(err, saveErr) {
		t.Fatalf("Connect() error = %v, want it to wrap %v", err, saveErr)
	}
	if !store.saveCalled {
		t.Error("SaveRefreshToken was not called")
	}
}

func TestDisconnect_DeletesToken(t *testing.T) {
	store := &mockTokenStore{}
	c := New(store, newAuthForExchange())

	if err := c.Disconnect(context.Background(), "user-1"); err != nil {
		t.Fatalf("Disconnect() error = %v, want nil", err)
	}
	if store.deletedUser != "user-1" {
		t.Errorf("deleted userID = %q, want %q", store.deletedUser, "user-1")
	}
}

func TestDisconnect_NotConnected(t *testing.T) {
	store := &mockTokenStore{deleteErr: ErrNotConnected}
	c := New(store, newAuthForExchange())

	if err := c.Disconnect(context.Background(), "user-1"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Disconnect() error = %v, want ErrNotConnected", err)
	}
}

func TestMissingScopes(t *testing.T) {
	tests := []struct {
		name     string
		required []string
		granted  []string
		want     []string
	}{
		{
			name:     "all granted",
			required: RequiredScopes,
			granted:  RequiredScopes,
			want:     nil,
		},
		{
			name:     "none granted",
			required: []string{"a", "b"},
			granted:  nil,
			want:     []string{"a", "b"},
		},
		{
			name:     "partial grant returns only the gap",
			required: []string{"a", "b", "c"},
			granted:  []string{"b", "x"},
			want:     []string{"a", "c"},
		},
		{
			name:     "extra granted scopes are ignored",
			required: []string{"a"},
			granted:  []string{"a", "b", "c"},
			want:     nil,
		},
		{
			name:     "order of granted does not matter",
			required: []string{"a", "b"},
			granted:  []string{"b", "a"},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := missingScopes(tt.required, tt.granted)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingScopes(%v, %v) = %v, want %v", tt.required, tt.granted, got, tt.want)
			}
		})
	}
}

func TestGrantedScopes(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]interface{}
		want  []string
	}{
		{
			name:  "space separated scope string",
			extra: map[string]interface{}{"scope": "user-read-playback-state user-modify-playback-state"},
			want:  []string{"user-read-playback-state", "user-modify-playback-state"},
		},
		{
			name:  "single scope",
			extra: map[string]interface{}{"scope": "playlist-read-private"},
			want:  []string{"playlist-read-private"},
		},
		{
			name:  "empty scope string",
			extra: map[string]interface{}{"scope": ""},
			want:  []string{},
		},
		{
			name:  "extra whitespace is collapsed",
			extra: map[string]interface{}{"scope": "  a   b  "},
			want:  []string{"a", "b"},
		},
		{
			name:  "no scope field",
			extra: map[string]interface{}{},
			want:  []string{},
		},
		{
			name:  "scope field of unexpected type",
			extra: map[string]interface{}{"scope": 42},
			want:  []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := (&oauth2.Token{}).WithExtra(tt.extra)
			got := grantedScopes(token)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("grantedScopes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScopeErrorWrapsSentinel(t *testing.T) {
	err := error(&ScopeError{
		Granted: []string{"a"},
		Missing: []string{"b", "c"},
	})

	if !errors.Is(err, ErrMissingScopes) {
		t.Errorf("errors.Is(err, ErrMissingScopes) = false, want true")
	}

	var se *ScopeError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As(err, *ScopeError) = false, want true")
	}
	if !reflect.DeepEqual(se.Missing, []string{"b", "c"}) {
		t.Errorf("se.Missing = %v, want [b c]", se.Missing)
	}
}

func TestSentinelFor(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{
			name: "404 no active device",
			err:  spotify.Error{Status: 404, Message: "Player command failed: No active device found"},
			want: ErrNoActiveDevice,
		},
		{
			name: "404 other reason is not mapped",
			err:  spotify.Error{Status: 404, Message: "Not found"},
			want: nil,
		},
		{
			name: "403 premium required",
			err:  spotify.Error{Status: 403, Message: "Player command failed: Premium required"},
			want: ErrPremiumRequired,
		},
		{
			name: "403 other reason is not mapped",
			err:  spotify.Error{Status: 403, Message: "Forbidden"},
			want: nil,
		},
		{
			name: "429 rate limited",
			err:  spotify.Error{Status: 429, Message: "API rate limit exceeded"},
			want: ErrRateLimited,
		},
		{
			name: "unrecognized status",
			err:  spotify.Error{Status: 500, Message: "Internal server error"},
			want: nil,
		},
		{
			name: "non-API error",
			err:  errors.New("boom"),
			want: nil,
		},
		{
			name: "wrapped API error is still matched",
			err:  fmt.Errorf("play: %w", spotify.Error{Status: 429, Message: "slow down"}),
			want: ErrRateLimited,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sentinelFor(tt.err); got != tt.want {
				t.Errorf("sentinelFor() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWrapError(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		if got := wrapError("play", nil); got != nil {
			t.Errorf("wrapError(nil) = %v, want nil", got)
		}
	})

	t.Run("joins sentinel and keeps original in the chain", func(t *testing.T) {
		api := spotify.Error{Status: 404, Message: "Player command failed: No active device found"}
		err := wrapError("play", api)

		if !errors.Is(err, ErrNoActiveDevice) {
			t.Errorf("errors.Is(err, ErrNoActiveDevice) = false, want true")
		}
		var apiErr spotify.Error
		if !errors.As(err, &apiErr) {
			t.Errorf("errors.As(err, *spotify.Error) = false, want true")
		}
	})

	t.Run("plain error is annotated without a sentinel", func(t *testing.T) {
		base := errors.New("boom")
		err := wrapError("seek", base)

		if !errors.Is(err, base) {
			t.Errorf("errors.Is(err, base) = false, want true")
		}
		for _, sentinel := range []error{ErrNoActiveDevice, ErrPremiumRequired, ErrRateLimited} {
			if errors.Is(err, sentinel) {
				t.Errorf("errors.Is(err, %v) = true, want false", sentinel)
			}
		}
	})
}

// getterStore is a TokenStore whose GetRefreshToken outcome is scripted, for
// exercising Connected without expecting saves or deletes.
type getterStore struct {
	token string
	err   error
}

func (g *getterStore) GetRefreshToken(context.Context, string) (string, error) {
	return g.token, g.err
}
func (g *getterStore) SaveRefreshToken(context.Context, string, string) error {
	return errors.New("getterStore: SaveRefreshToken not expected")
}
func (g *getterStore) DeleteRefreshToken(context.Context, string) error {
	return errors.New("getterStore: DeleteRefreshToken not expected")
}

// Connected maps the store's three outcomes for the UI status check: a stored
// token is true, ErrNotConnected is false-without-error, anything else is an
// infrastructure error.
func TestConnected(t *testing.T) {
	tests := []struct {
		name    string
		store   *getterStore
		want    bool
		wantErr bool
	}{
		{name: "connected", store: &getterStore{token: "refresh-xyz"}, want: true},
		{name: "not connected", store: &getterStore{err: ErrNotConnected}, want: false},
		{name: "store failure", store: &getterStore{err: errors.New("boom")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(tt.store, newAuthForExchange())
			got, err := c.Connected(context.Background(), "user")
			if (err != nil) != tt.wantErr {
				t.Fatalf("Connected err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Connected = %v, want %v", got, tt.want)
			}
		})
	}
}
