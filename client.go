package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zmb3/spotify/v2"
	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"golang.org/x/oauth2"
)

// ErrNotConnected indicates the user has no stored Spotify credentials,
// i.e. they have not completed the OAuth connect flow. TokenStore
// implementations should return this from GetRefreshToken when no token
// exists, so consumers can route the user into the login flow.
var ErrNotConnected = errors.New("spotify: user not connected")

// ErrMissingScopes indicates the user completed the OAuth flow but did not
// grant every scope the library needs (see RequiredScopes). Detected at
// Exchange time so consumers can fail at connect, not months later when a
// playback call returns 403. Inspect the granted/missing sets via a
// *ScopeError obtained with errors.As.
var ErrMissingScopes = errors.New("spotify: missing required scopes")

// RequiredScopes are the OAuth scopes every capability in this library needs.
// Pass them to the Authenticator so the consent screen requests the right
// permissions:
//
//	auth := spotifyauth.New(
//	    spotifyauth.WithClientID(id),
//	    spotifyauth.WithClientSecret(secret),
//	    spotifyauth.WithRedirectURL(redirectURL),
//	    spotifyauth.WithScopes(spotify.RequiredScopes...),
//	)
//
// Mapping of scope to capability:
//   - user-modify-playback-state: Play, Pause, Resume, SetVolume
//   - user-read-playback-state:   Devices
//   - playlist-read-private:      UserPlaylists
//
// Search (SearchTracks, SearchPlaylists, PlaylistTracks) needs no scope.
var RequiredScopes = []string{
	spotifyauth.ScopeUserModifyPlaybackState,
	spotifyauth.ScopeUserReadPlaybackState,
	spotifyauth.ScopePlaylistReadPrivate,
}

// ScopeError reports which scopes were granted versus which are missing after
// an OAuth exchange. It wraps ErrMissingScopes; match it with errors.As.
type ScopeError struct {
	Granted []string
	Missing []string
}

func (e *ScopeError) Error() string {
	return fmt.Sprintf("spotify: missing required scopes %v (granted %v) — reconnect granting full permissions", e.Missing, e.Granted)
}

func (e *ScopeError) Unwrap() error { return ErrMissingScopes }

// TokenStore persists Spotify OAuth tokens on behalf of a user.
// Implement this interface to provide your own storage backend.
// GetRefreshToken and DeleteRefreshToken must return ErrNotConnected when no
// token exists for userID.
type TokenStore interface {
	GetRefreshToken(ctx context.Context, userID string) (string, error)
	// SaveRefreshToken stores (or replaces) the refresh token for userID.
	// It is called after a successful OAuth exchange.
	SaveRefreshToken(ctx context.Context, userID, refreshToken string) error
	// DeleteRefreshToken removes the stored refresh token for userID.
	DeleteRefreshToken(ctx context.Context, userID string) error
}

// Client provides access to Spotify on behalf of a user.
type Client struct {
	tokenStore TokenStore
	auth       *spotifyauth.Authenticator
}

func New(tokenStore TokenStore, auth *spotifyauth.Authenticator) *Client {
	return &Client{
		tokenStore: tokenStore,
		auth:       auth,
	}
}

// Track represents a Spotify track.
type Track struct {
	ID      string
	Name    string
	Artists []string
	URI     string
	URL     string
}

// Playlist represents a Spotify playlist.
type Playlist struct {
	ID          string
	Name        string
	Description string
	Total       int
	URL         string
}

// Device represents an active Spotify playback device.
type Device struct {
	ID       string
	Name     string
	Type     string
	IsActive bool
	Volume   int
}

// Playback describes what a user is currently listening to. Track is nil when
// nothing identifiable is loaded (e.g. an ad or a podcast episode), and
// ContextURI/ContextType are empty when playback is not driven by an album,
// playlist, or artist. CurrentPlayback returns a nil *Playback when no device
// is active at all.
type Playback struct {
	Track       *Track
	Device      Device
	IsPlaying   bool
	ProgressMs  int
	ContextURI  string // URI of the album/playlist/artist driving playback, if any
	ContextType string // "album", "playlist", or "artist"; empty if none
}

// AuthOption customizes a single AuthURL/Exchange call. It exists so callers can
// tweak the OAuth request without this package leaking golang.org/x/oauth2 types.
type AuthOption func(*authConfig)

type authConfig struct{ params []oauth2.AuthCodeOption }

// WithRedirectURI overrides the redirect_uri for this single call, instead of the
// one baked into the Authenticator via spotifyauth.WithRedirectURL. Use it when the
// public callback address is owned by a component in front of this service (e.g. a
// reverse proxy / API gateway) rather than known at construction time.
//
// OAuth requires the redirect_uri sent to Exchange to be identical to the one sent
// to AuthURL (RFC 6749 §4.1.3), so the caller must thread the same value through
// both calls — in practice by carrying it in the signed state. The value must also
// be one of the redirect URIs registered in the Spotify Developer Dashboard.
func WithRedirectURI(uri string) AuthOption {
	return func(c *authConfig) {
		c.params = append(c.params, oauth2.SetAuthURLParam("redirect_uri", uri))
	}
}

// AuthURL returns the Spotify Accounts authorization URL the user must visit
// to grant access. state is handed to Spotify and returned verbatim on the
// callback; consumers use it to correlate the callback with a user and to
// guard against CSRF. The redirect URI and scopes are taken from the
// Authenticator supplied to New, unless overridden with WithRedirectURI.
func (c *Client) AuthURL(state string, opts ...AuthOption) string {
	var cfg authConfig
	for _, o := range opts {
		o(&cfg)
	}
	return c.auth.AuthURL(state, cfg.params...)
}

// Exchange completes the OAuth flow by trading the authorization code from the
// Spotify callback for tokens, returning the refresh token to persist via
// TokenStore.SaveRefreshToken. The code is single-use; a second Exchange with
// the same code is rejected by Spotify. If WithRedirectURI was passed to AuthURL,
// the same value must be passed here (OAuth requires the two to match).
func (c *Client) Exchange(ctx context.Context, code string, opts ...AuthOption) (string, error) {
	var cfg authConfig
	for _, o := range opts {
		o(&cfg)
	}
	token, err := c.auth.Exchange(ctx, code, cfg.params...)
	if err != nil {
		return "", fmt.Errorf("exchange code: %w", err)
	}
	if token.RefreshToken == "" {
		return "", errors.New("spotify: exchange returned no refresh token")
	}
	granted := grantedScopes(token)
	if missing := missingScopes(RequiredScopes, granted); len(missing) > 0 {
		return "", &ScopeError{Granted: granted, Missing: missing}
	}
	return token.RefreshToken, nil
}

// Connect completes the OAuth flow for userID: it trades code for tokens via
// Exchange and persists the resulting refresh token through TokenStore. From the
// caller's point of view this is atomic — no token is handed back to manage. Use
// it instead of Exchange + SaveRefreshToken when the consumer has no need for the
// raw token. If WithRedirectURI was passed to AuthURL, the same value must be
// passed here (OAuth requires the two to match).
func (c *Client) Connect(ctx context.Context, userID, code string, opts ...AuthOption) error {
	refreshToken, err := c.Exchange(ctx, code, opts...)
	if err != nil {
		return err
	}
	return c.tokenStore.SaveRefreshToken(ctx, userID, refreshToken)
}

// Disconnect undoes Connect: it removes the user's stored refresh token, so
// every capability returns ErrNotConnected until the user completes a new
// OAuth flow. Returns ErrNotConnected when the user was not connected.
//
// Only this side forgets the token — the grant itself stays listed on the
// user's Spotify account (spotify.com/account/apps) until they revoke it
// there.
func (c *Client) Disconnect(ctx context.Context, userID string) error {
	return c.tokenStore.DeleteRefreshToken(ctx, userID)
}

// grantedScopes reads the scopes Spotify actually granted from the token
// exchange response. Spotify returns them as a space-separated "scope" field,
// which oauth2 surfaces via Token.Extra. Reflects what the user approved, not
// merely what was requested.
func grantedScopes(token *oauth2.Token) []string {
	raw, _ := token.Extra("scope").(string)
	return strings.Fields(raw)
}

// missingScopes returns the elements of required not present in granted.
func missingScopes(required, granted []string) []string {
	have := make(map[string]struct{}, len(granted))
	for _, s := range granted {
		have[s] = struct{}{}
	}
	var missing []string
	for _, s := range required {
		if _, ok := have[s]; !ok {
			missing = append(missing, s)
		}
	}
	return missing
}

func (c *Client) clientFor(ctx context.Context, userID string) (*spotify.Client, error) {
	httpClient, err := c.httpClientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	return spotify.New(httpClient), nil
}

// httpClientFor returns the user's token-refreshing *http.Client for talking to
// the Spotify Web API directly. Playlist reads bypass zmb3 (see getJSON) because
// v2.4.3 predates Spotify's 2026 field migration and still speaks the retired
// shapes; every other capability goes through the zmb3 client from clientFor.
func (c *Client) httpClientFor(ctx context.Context, userID string) (*http.Client, error) {
	refreshToken, err := c.tokenStore.GetRefreshToken(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get token for user %s: %w", userID, err)
	}
	return c.auth.Client(ctx, &oauth2.Token{RefreshToken: refreshToken}), nil
}

// apiBase is the Spotify Web API root. The oauth2 http client only injects the
// bearer token, so callers pass absolute URLs.
const apiBase = "https://api.spotify.com/v1"

// getJSON performs an authenticated GET against the Spotify Web API and decodes
// a 200 body into out. A non-200 is decoded into a spotify.Error so that
// wrapError/sentinelFor classify it exactly like a zmb3-originated error.
func getJSON(ctx context.Context, hc *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return spotify.Error{Status: resp.StatusCode, Message: body.Error.Message}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ErrRateLimited means Spotify is throttling the application (HTTP 429). It can
// surface from any call, so it lives here rather than with a single feature.
// Back off and retry. Match it with errors.Is.
var ErrRateLimited = errors.New("spotify: rate limited")

// wrapError annotates err with the operation name and, when it carries a
// recognizable Spotify API error, joins one of the package sentinels so callers
// can branch with errors.Is. The original error stays in the chain either way.
// It returns nil when err is nil so call sites can wrap unconditionally. Every
// feature routes its API errors through here.
func wrapError(op string, err error) error {
	if err == nil {
		return nil
	}
	if sentinel := sentinelFor(err); sentinel != nil {
		return fmt.Errorf("%s: %w: %w", op, sentinel, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// sentinelFor maps a Spotify API error to a package sentinel, or returns nil
// when none applies. Spotify encodes the precise cause in a "reason" field that
// zmb3 discards, so for 403/404 we disambiguate on the message text and accept
// that an unrecognized message falls through to no sentinel.
func sentinelFor(err error) error {
	var apiErr spotify.Error
	if !errors.As(err, &apiErr) {
		return nil
	}
	switch apiErr.Status {
	case http.StatusNotFound:
		if strings.Contains(apiErr.Message, "No active device") {
			return ErrNoActiveDevice
		}
	case http.StatusForbidden:
		if strings.Contains(strings.ToLower(apiErr.Message), "premium") {
			return ErrPremiumRequired
		}
	case http.StatusTooManyRequests:
		return ErrRateLimited
	}
	return nil
}
