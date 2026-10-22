# spotify

[![Go Reference](https://pkg.go.dev/badge/go.trueardian.com/spotify.svg)](https://pkg.go.dev/go.trueardian.com/spotify)
![Go 1.25+](https://img.shields.io/badge/go-1.25%2B-00ADD8)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A Go library for the [Spotify Web API](https://developer.spotify.com/documentation/web-api), on
behalf of your users.

## Install

```sh
go get go.trueardian.com/spotify
```

## The packages

| Package             | What it is                                                       | Provides             | Depends on |
| ------------------- | ---------------------------------------------------------------- | -------------------- | ---------- |
| `spotify`           | The Spotify client. OAuth, search, playback, per user.           | `*spotify.Client`    |            |
| `spotify/postgres`  | PostgreSQL implementation of `spotify.TokenStore`, over `pgx`.   | `spotify.TokenStore` | `spotify`  |
| `spotify/firestore` | Cloud Firestore implementation of `spotify.TokenStore`.          | `spotify.TokenStore` | `spotify`  |

```
postgres ──┬─▶ spotify
firestore ─┘
```

Only import what you need. Only the driver you import ends up in your binary. Below are the
explanations and guides.

## `package spotify`

So you want your app to find songs and put them on for your users, but don't want to deal with
OAuth, refresh tokens, and everything Spotify quietly changed under you? I got you.

Create an app in the [Spotify developer dashboard](https://developer.spotify.com/dashboard) and grab
its Client ID, Client Secret, and the redirect URI you registered there. You build the authenticator
yourself, so the secret never passes through this library, and you ask for `spotify.RequiredScopes`:

```go
auth := spotifyauth.New(
    spotifyauth.WithClientID(clientID),
    spotifyauth.WithClientSecret(clientSecret),
    spotifyauth.WithRedirectURL("https://you.example/spotify/callback"),
    spotifyauth.WithScopes(spotify.RequiredScopes...),
)

client := spotify.New(store, auth)
```

`spotifyauth` is `github.com/zmb3/spotify/v2/auth`. `store` is where each user's refresh token lives:
a `TokenStore`, and two come ready-made further down.

Every call takes a user ID. That's whatever identity your app already has for the person: a user ID,
an email, a tenant row. The library never looks inside it.

### Connecting a user

Before anything works, the user approves your app on Spotify once. Spotify then redirects back to
your server, not this library's, because there is no server here. You write two small handlers, and
the library does the Spotify half of each:

```go
// GET /spotify/connect, the user is already logged into your app
state := signJWT(userID, 5*time.Minute)
http.Redirect(w, r, client.AuthURL(state), http.StatusFound)

// GET /spotify/callback?code=…&state=…
userID, err := verifyJWT(r.URL.Query().Get("state"))
if err != nil {
    http.Error(w, "bad state", http.StatusBadRequest)
    return
}
err = client.Connect(ctx, userID, r.URL.Query().Get("code"))
```

Make `state` a short signed token with the user ID and an expiry, not the raw user ID. It's what
tells you whose callback this is, and a raw ID is guessable. Five minutes is plenty, Spotify's `code`
is single-use anyway.

`Connect` trades the code for a refresh token and saves it, so you never hold the token yourself.
Want it in hand anyway? `Exchange` does only the first half.

It also checks the scopes the user actually granted, not the ones you asked for. Untick one on the
consent screen and `Connect` fails right there, instead of a mystery 403 on playback weeks later:

```go
var scopeErr *spotify.ScopeError
if errors.As(err, &scopeErr) {
    // scopeErr.Missing is what they withheld, so send them through consent again
}
```

Is your public callback behind a gateway, so you don't know its address when you build the
authenticator? Pass `spotify.WithRedirectURI(uri)` to both `AuthURL` and `Connect`. OAuth wants the
two to match byte for byte, so carry the value in the signed `state`.

`client.Connected(ctx, userID)` tells you whether to show the "Connect Spotify" button. It only asks
your store, so a grant the user revoked on Spotify's side still reads as connected until its first
call fails. `client.Disconnect(ctx, userID)` forgets the token on your side. The grant itself stays
listed in the user's [Spotify account](https://www.spotify.com/account/apps/) until they remove it
there.

### Then just call it

```go
tracks, err := client.SearchTracks(ctx, userID, "Bohemian Rhapsody")
playlists, err := client.SearchPlaylists(ctx, userID, "lofi")
mine, err := client.UserPlaylists(ctx, userID)
tracks, err = client.PlaylistTracks(ctx, userID, playlistID)
```

You get the first page and that's it: 10 per search, 20 of the user's playlists, 50 tracks of a
playlist.

Search sends no `market`, so results are global. Asking for the user's market needs
`user-read-private`, one scope more than everything else here needs, and I'd rather not make you ask
your users for it. A region-locked track that slips through fails on `Play`, which tells you so.

The playlist calls skip zmb3 and read the Web API directly. Spotify's 2026 migration renamed a
playlist's `tracks` to `items`, and `zmb3/spotify/v2` v2.4.3 still reads the old shape, so through
it every playlist comes back empty. You won't notice the difference: the errors come out exactly the
same. Removed tracks and podcast episodes in a playlist are skipped.

Then the playback:

```go
devices, err := client.Devices(ctx, userID)
now, err := client.CurrentPlayback(ctx, userID) // nil when nothing is active

err = client.Play(ctx, userID, spotify.PlayRequest{URI: tracks[0].URI})
err = client.Pause(ctx, userID)
err = client.Resume(ctx, userID)
err = client.Next(ctx, userID)
err = client.Previous(ctx, userID)
err = client.Seek(ctx, userID, 90_000) // milliseconds
err = client.SetVolume(ctx, userID, 40) // percent
```

`Play` is one request for every way of starting music. What it does depends on which fields you set:

```go
spotify.PlayRequest{URI: "spotify:track:…"}    // that track, alone
spotify.PlayRequest{URI: "spotify:playlist:…"} // the whole playlist, same for an album or artist
spotify.PlayRequest{ContextURI: "spotify:playlist:…", URI: "spotify:track:…"} // that track, inside the playlist
spotify.PlayRequest{}                          // resume whatever is loaded
```

The third one is what you want after `PlaylistTracks`. Next and previous stay inside the playlist,
instead of the music stopping after one song.

`DeviceID` picks where it plays, and empty means the active device. Nothing active? A device that's
open but idle still shows up in `Devices`, so play on it by ID, or move the current session onto it
with `client.TransferPlayback(ctx, userID, deviceID, true)`, queue and position included.

### When it fails

The errors worth branching on, matched with `errors.Is`:

| Error                | Means                                            | So                                                   |
| -------------------- | ------------------------------------------------ | ---------------------------------------------------- |
| `ErrNotConnected`    | No token stored, never connected or disconnected | Send them through `Connect`                          |
| `ErrNoActiveDevice`  | Spotify has nowhere to play                      | Pick one from `Devices`, or ask them to open Spotify |
| `ErrPremiumRequired` | Playback control is Premium-only                 | Tell them                                            |
| `ErrRateLimited`     | Spotify is throttling you                        | Back off and retry                                   |

Spotify's own error stays in the chain, so `errors.As` to zmb3's `spotify.Error` still works.

Spotify puts the real cause in a `reason` field that zmb3 throws away, and a 403 or 404 can each mean
two different things, so no active device and Premium required are recognized from the message text.
A message it doesn't recognize gets no sentinel at all. You get the plain error, never a wrong one.

### No token cache

Every call reads the refresh token from your store and gets a fresh access token. That's one extra
round trip per call, and I'm fine with it: this library was built to be an AI agent's tools, called
a few times per user request, not hundreds of times a second. What you get for it is no stale state
anywhere. A `Disconnect` or a reconnect takes effect on the very next call. If you're building a
busy playback service, this is the trade-off that flips.

## `package postgres`

PostgreSQL, over `pgx`. It takes your `*pgxpool.Pool` and the DSN you built it from:

```go
store := postgres.New(pool, dsn)
if err := store.Migrate(); err != nil {
    return err
}
```

`Migrate()` creates the `spotify_tokens` table from migrations embedded in the binary, through
[golang-migrate](https://github.com/golang-migrate/migrate), which records the version so each
migration runs once. golang-migrate opens its own connection, hence the DSN: rebuilding it from the
pool is fragile. Leave `Migrate()` out if migrations are your deploy's job.

Saving again for the same user replaces the token, and `Disconnect` really deletes the row.

## `package firestore`

Or Firestore, one document per user, keyed by the user ID:

```go
store := firestore.New(fsClient)
```

No migrations, so no error to handle. The collection is `spotify_tokens`; rename it with
`firestore.WithCollection("...")`. Do note the import name collides with Google's own `firestore`
package, so alias one of them.

The user ID goes in as the document ID untouched, so an ID Firestore forbids (empty, `.`, `..`,
anything with a `/`, `__like_this__`, over 1500 bytes) is rejected before Firestore ever sees it.
`created_at` is written on the first save and kept when the token is replaced.

## With ADK

Using Google's Agent Development Kit? The tools live in a separate module, so this one stays free of
it:

```go
import spotifyadk "go.trueardian.com/agentkit/spotify/adk"

tools, err := spotifyadk.Tools(client)
```

That hands your agent `search_tracks`, `my_playlists`, `playlist_tracks`, `now_playing`,
`my_devices`, `play`, `transfer_playback`, `pause`, `skip_next`, `skip_previous` and `set_volume`,
with the prompting already written.

## What this library doesn't do

- **Run an HTTP server.** Explained above. The callback is yours, so it fits behind your own routing,
  auth and TLS instead of fighting them.
- **Paginate.** One request from a user needs the first page, not the whole catalog. Open an issue if
  you have a real case for more.
- **Cache tokens.** Explained above.
- **Encrypt tokens.** Both stores keep the refresh token exactly as Spotify gave it. Need it
  encrypted at rest? Wrap the `TokenStore`.
- **Save a rotated refresh token.** Only the PKCE flow rotates it, and this library uses the
  Authorization Code flow, which doesn't.
- **Wrap all of Spotify.** Only what an agent needs to find and play music, so far. Open an issue
  for the endpoint you need, I'll add it.
- **Ship more stores.** Only Postgres and Firestore. Need another? Write your own, buddy:
  `TokenStore` is three methods (`GetRefreshToken`, `SaveRefreshToken`, `DeleteRefreshToken`), and
  the only contract that matters is returning `spotify.ErrNotConnected` from get and delete when the
  user has no token. Pull requests are welcome too, though I'm fairly strict about how code is
  written here, so expect me to refactor yours before merging.

## License

[MIT](LICENSE) © 2026 Ardian
