# spotify

Go module `go.naturallyfunny.dev/spotify` — a reusable library for Spotify Web API
integration. Interface-based, not tied to a single database or application. Built to be
used as a tool invoked by an AI agent: low traffic, one action per user intent (search a
song, play, pause).

## Installation

```sh
go get go.naturallyfunny.dev/spotify
```

## Concepts

`Client` takes two injected dependencies:

- **`TokenStore`** — per-user refresh-token storage. An interface (consumer-defined);
  ready-made implementations live in the `postgres` and `firestore` packages.
- **`*spotifyauth.Authenticator`** — OAuth credentials (client ID/secret, redirect URI,
  scopes). The library does not build credentials itself.

## Setup

```go
auth := spotifyauth.New(
    spotifyauth.WithClientID(clientID),
    spotifyauth.WithClientSecret(clientSecret),
    spotifyauth.WithRedirectURL("https://app-consumer/spotify/callback"),
    spotifyauth.WithScopes(spotify.RequiredScopes...),
)

store := postgres.New(pool, dsn)
if err := store.Migrate(); err != nil {
    log.Fatal(err)
}

client := spotify.New(store, auth)
```

Or back tokens with Cloud Firestore instead (alias one of the two firestore imports;
there is nothing to migrate):

```go
import (
    gcfs "cloud.google.com/go/firestore"
    "go.naturallyfunny.dev/spotify/firestore"
)

fs, err := gcfs.NewClient(ctx, projectID)
store := firestore.New(fs) // one document per user in "spotify_tokens" (WithCollection overrides)
```

## Connecting a user (OAuth)

Before any command can run, the user must connect their Spotify account. The library
provides `AuthURL`, `Exchange`, and `SaveRefreshToken`; the tricky Spotify parts live in
the library, while the HTTP layer and user session stay with the consumer.

| Library (provided)                     | Consumer (build yourself)                              |
| -------------------------------------- | ------------------------------------------------------ |
| `AuthURL(state)` → Spotify link        | HTTP endpoints `/spotify/connect` & `/spotify/callback` |
| `Exchange(ctx, code)` → refresh token  | Generate & verify `state`                              |
| `SaveRefreshToken(ctx, userID, token)` | Know "which user this callback is for"                 |

The library does **not** run an HTTP server (intentionally HTTP-free). The Spotify callback
must land on an HTTP address you own.

### Flow

1. Client: a "Connect Spotify" button → calls the consumer's `GET /spotify/connect`.
2. Consumer calls `AuthURL(state)` → gets a Spotify link → redirects the user there.
3. User logs in & clicks Allow on Spotify's page.
4. Spotify redirects to `GET /spotify/callback?code=...&state=...`.
5. Consumer verifies `state`, calls `Exchange(code)`, then `SaveRefreshToken`.

From this point `GetRefreshToken` is populated → every command (Play, Pause, etc.) works.

To undo the flow, `Disconnect(ctx, userID)` deletes the stored refresh token
(`ErrNotConnected` when there was none). Only this side forgets the token — the grant
stays listed on the user's Spotify account (spotify.com/account/apps) until revoked there.

### Redirect URI: set once, or override per call

Spotify requires the redirect URI to be **registered first** in the Developer Dashboard, and
the URL sent must match **exactly**. For the simplest deployment it is fixed, so set it once on
the authenticator (see Setup).

When the public callback address is **not owned by this service** — e.g. it sits behind a
reverse proxy / API gateway, and the edge owns the callback URL — bake nothing in and let the
caller pass it per call with `spotify.WithRedirectURI`:

```go
url := client.AuthURL(state, spotify.WithRedirectURI(callbackURL))
// ...later, in the callback handler, with the same value recovered from state:
refreshToken, err := client.Exchange(ctx, code, spotify.WithRedirectURI(callbackURL))
```

**The same value must be used at `AuthURL` and `Exchange`.** OAuth requires the `redirect_uri`
sent to the token endpoint to be identical to the one sent to the authorize endpoint (RFC 6749
§4.1.3), so the caller threads it through both calls — in practice by carrying it in the signed
`state`. The value must also be a redirect URI registered in the Developer Dashboard.

Omitting `WithRedirectURI` preserves the default: the redirect URI configured on the
authenticator. If you omit it at construction, every call must supply `WithRedirectURI` (Spotify
rejects an empty `redirect_uri`).

### `state` — use a JWT (stateless, no server-side storage)

`state` is a string the consumer hands to Spotify, returned verbatim on the callback. It
serves two purposes: (1) know which `userID` the callback is for, (2) prevent callback
forgery (CSRF).

**Do not** use a raw `userID` as `state` — it is guessable and leaks in URLs/logs. Use a
short **JWT** containing `uid` + `exp`, HMAC-signed with the app secret:

- **Short exp** (~5 min) — the user clicks Allow within seconds.
- **Secret** from an env var, never hardcoded.
- **Minimal payload** (`uid`, `exp`) — a JWT is only signed, its contents are still readable
  by anyone (base64, not encrypted). Don't put sensitive data in it.
- **Replay:** a JWT is not inherently single-use, but it is safe here because Spotify's
  `code` is single-use (a second Exchange with the same code is rejected). A short exp + a
  single-use code is enough.

```go
// GET /spotify/connect (user is already logged in to the app → userID is known)
state := signJWT(userID, 5*time.Minute)
http.Redirect(w, r, client.AuthURL(state), http.StatusFound)

// GET /spotify/callback?code=...&state=...
userID, err := verifyJWT(r.URL.Query().Get("state")) // reject if invalid/expired

refreshToken, err := client.Exchange(ctx, r.URL.Query().Get("code"))
var scopeErr *spotify.ScopeError
switch {
case errors.As(err, &scopeErr):
    // User did not grant all permissions → ask them to reconnect. scopeErr.Missing
    // holds the missing scopes. Do NOT swallow this silently.
    redirectToReconnect(w, r)
    return
case err != nil:
    http.Error(w, "spotify connect failed", http.StatusBadGateway)
    return
}
store.SaveRefreshToken(ctx, userID, refreshToken)
```

### Scopes — use `spotify.RequiredScopes`

The library exports `spotify.RequiredScopes` (the union of scopes across all capabilities)
and **validates them in `Exchange`**. So:

- Pass `spotify.RequiredScopes...` to `WithScopes` when building the authenticator.
- If the user does not approve some of them, `Exchange` returns a `*ScopeError` (wrapping
  `ErrMissingScopes`) — **failing at connect time**, not silently as a 403 months later
  during playback. Handle it with `errors.As` (see the callback above) and prompt the user
  to reconnect.

Scope → capability mapping (for reference):

- `user-modify-playback-state` → Play / Pause / Resume / Next / Previous / Seek / SetVolume
- `user-read-playback-state` → Devices / CurrentPlayback
- `playlist-read-private` → UserPlaylists

Note: `Exchange` requires **all three** scopes. Even a search-only consumer must grant them
all — a deliberate choice for the single-AI-agent use case that uses every capability.

## Usage

```go
tracks, err := client.SearchTracks(ctx, userID, "Queen")

// Play a single track, or a whole album/playlist/artist, on a device.
err = client.Play(ctx, userID, spotify.PlayRequest{DeviceID: deviceID, URI: tracks[0].URI})

// Play a track *within* a playlist context so skip next/previous stay in it.
err = client.Play(ctx, userID, spotify.PlayRequest{
    DeviceID:   deviceID,
    ContextURI: "spotify:playlist:...",
    URI:        tracks[0].URI, // the track to start at inside the playlist
})

// Wake an available-but-idle device by moving the current session onto it.
err = client.TransferPlayback(ctx, userID, deviceID, true)

err = client.Pause(ctx, userID)
err = client.Next(ctx, userID)

pb, err := client.CurrentPlayback(ctx, userID) // nil if no active device
```

## Error sentinels

Match with `errors.Is` / `errors.As` to decide what to do:

| Sentinel             | Meaning                                          | Suggested reaction          |
| -------------------- | ------------------------------------------------ | --------------------------- |
| `ErrNotConnected`    | User not connected (TokenStore empty)            | Route into the OAuth flow   |
| `ScopeError`         | Connected but missing scopes (wraps `ErrMissingScopes`) | Reconnect            |
| `ErrNoActiveDevice`  | No active device (404 `NO_ACTIVE_DEVICE`)        | Tell the user to open Spotify |
| `ErrPremiumRequired` | Playback requires a Premium account (403)        | Inform the user             |
| `ErrRateLimited`     | Throttled by Spotify (429)                       | Back off & retry            |

## Migrations

The reference store (`postgres`) uses `golang-migrate`; SQL is embedded via `//go:embed`.
`store.Migrate()` runs pending migrations. Schema: a single `spotify_tokens` table
(`owner text`, `refresh_token`).

The `firestore` store has no migrations — Firestore is schemaless, and the
`spotify_tokens` collection appears with the first saved token.
