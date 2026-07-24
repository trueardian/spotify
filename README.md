# spotify

[![Go Reference](https://pkg.go.dev/badge/go.naturallyfunny.dev/spotify.svg)](https://pkg.go.dev/go.naturallyfunny.dev/spotify)
[![Go 1.25](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A small, interface-based Go library for the Spotify Web API. It hands the caller
a per-user `Client` and keeps two things out of its own way on purpose: **where
tokens live** (a `TokenStore` interface you implement, or plug in the bundled
`postgres`/`firestore` stores) and **how OAuth callbacks reach you** (no HTTP
server — the library never listens on a socket).

```go
auth := spotifyauth.New(
    spotifyauth.WithClientID(id),
    spotifyauth.WithClientSecret(secret),
    spotifyauth.WithRedirectURL(redirectURL),
    spotifyauth.WithScopes(spotify.RequiredScopes...),
)

client := spotify.New(postgres.New(pool, dsn), auth)

tracks, err := client.SearchTracks(ctx, userID, "Queen")
err = client.Play(ctx, userID, spotify.PlayRequest{DeviceID: dev, URI: tracks[0].URI})
```

**Who calls it, and how.** This library is built to be driven by an **AI agent
acting as a tool**: sporadic calls, one action per user intent (*find a song*,
*play it*, *pause*), low volume. That traffic shape is the single premise the
design optimizes for, and several decisions below point back to it as their
justification — so it is stated here up front, not buried. If you need a
high-throughput playback service, some of these trade-offs would flip; this
README says exactly where and why.

---

## Contents

- [Why this shape](#why-this-shape)
- [At a glance](#at-a-glance)
- [Install](#install)
- [Quick start](#quick-start)
- [Connecting a user (OAuth)](#connecting-a-user-oauth)
- [Error sentinels](#error-sentinels)
- [Design decisions & trade-offs](#design-decisions--trade-offs)
- [Design principles](#design-principles)
- [Non-goals](#non-goals)
- [Token stores](#token-stores)
- [Compatibility](#compatibility)
- [Verification](#verification)
- [Development](#development)
- [Layout](#layout)
- [License](#license)

---

## Why this shape

The whole library follows from one boundary decision: **the caller owns storage
and HTTP; the library owns Spotify's quirks.**

A Spotify integration has three moving parts — where you keep refresh tokens,
how the OAuth redirect reaches your process, and the pile of undocumented edge
cases in the Web API itself (retired field names, `204` meaning "nothing
playing", `403` that could be *premium required* or *insufficient scope*). Only
the third is Spotify-specific and worth centralizing. The first two are the
consumer's architecture, and a library that dictates them — "you must use this
table", "let me run your HTTP server" — stops being reusable.

So `Client` takes exactly two injected dependencies and builds nothing itself:

```go
func New(tokenStore TokenStore, auth *spotifyauth.Authenticator) *Client
```

- **`TokenStore`** — a three-method interface (`Get`/`Save`/`DeleteRefreshToken`)
  you satisfy however you store data. Two implementations ship in-tree.
- **`*spotifyauth.Authenticator`** — your OAuth client credentials, constructed
  by you, so the library never sees or assembles a client secret on its own.

Everything the library *does* keep — the Web API edge cases — is packaged as
typed results (`Track`, `Playlist`, `Playback`, `Device`) and matchable error
sentinels, so the caller never parses a Spotify JSON body or an HTTP status.

## At a glance

Everything the `Client` exposes, the scope each capability needs, and the wire
path it takes. "zmb3" is the [`zmb3/spotify/v2`](https://github.com/zmb3/spotify)
client; "stdlib" means a hand-rolled `net/http` call (see the playlist decision
[below](#4-two-playlist-reads-bypass-the-upstream-client)).

| Capability | Method | OAuth scope | Path |
| --- | --- | --- | --- |
| Search tracks | `SearchTracks` | *(none)* | zmb3 |
| Search playlists | `SearchPlaylists` | *(none)* | stdlib |
| A user's playlists | `UserPlaylists` | `playlist-read-private` | stdlib |
| Tracks in a playlist | `PlaylistTracks` | *(none)* | stdlib |
| List devices | `Devices` | `user-read-playback-state` | zmb3 |
| What's playing now | `CurrentPlayback` | `user-read-playback-state` | zmb3 |
| Play / resume in a context | `Play` | `user-modify-playback-state` | zmb3 |
| Move session to a device | `TransferPlayback` | `user-modify-playback-state` | zmb3 |
| Pause / Next / Previous / Seek / SetVolume | *(each)* | `user-modify-playback-state` | zmb3 |
| OAuth lifecycle | `AuthURL` / `Exchange` / `Connect` / `Disconnect` / `Connected` | — | oauth2 |

The three scopes are exported as `spotify.RequiredScopes`; pass them to the
authenticator and `Exchange` validates them (see [OAuth](#connecting-a-user-oauth)).

## Install

```sh
go get go.naturallyfunny.dev/spotify
```

Requires Go 1.25+. The bundled stores pull their own drivers only when you import
them — a consumer that implements `TokenStore` itself and never imports
`postgres`/`firestore` does not compile in `pgx` or the Firestore SDK.

## Quick start

End-to-end wiring, including the failure path that is part of the contract:

```go
package main

import (
    "context"
    "errors"
    "log"

    "go.naturallyfunny.dev/spotify"
    "go.naturallyfunny.dev/spotify/postgres"

    "github.com/jackc/pgx/v5/pgxpool"
    spotifyauth "github.com/zmb3/spotify/v2/auth"
)

func main() {
    ctx := context.Background()

    pool, err := pgxpool.New(ctx, dsn)
    if err != nil {
        log.Fatal(err)
    }
    store := postgres.New(pool, dsn)
    if err := store.Migrate(); err != nil { // creates spotify_tokens if absent
        log.Fatal(err)
    }

    auth := spotifyauth.New(
        spotifyauth.WithClientID(clientID),
        spotifyauth.WithClientSecret(clientSecret),
        spotifyauth.WithRedirectURL("https://you.example/spotify/callback"),
        spotifyauth.WithScopes(spotify.RequiredScopes...),
    )
    client := spotify.New(store, auth)

    // ...after the user has connected (see the OAuth section)...
    tracks, err := client.SearchTracks(ctx, userID, "Queen")
    if err != nil {
        log.Fatal(err)
    }

    err = client.Play(ctx, userID, spotify.PlayRequest{URI: tracks[0].URI})
    switch {
    case errors.Is(err, spotify.ErrNoActiveDevice):
        log.Println("ask the user to open Spotify on a device first")
    case errors.Is(err, spotify.ErrPremiumRequired):
        log.Println("playback needs a Premium account")
    case err != nil:
        log.Fatal(err)
    }
}
```

Prefer Cloud Firestore? Swap the store; there is nothing to migrate:

```go
import (
    gcfs "cloud.google.com/go/firestore"
    "go.naturallyfunny.dev/spotify/firestore"
)

fs, _ := gcfs.NewClient(ctx, projectID)
store := firestore.New(fs) // one doc per user in "spotify_tokens"; WithCollection overrides
```

### Playing: one method, three intents

`Play` collapses "play a track", "play a whole album/playlist", and "play a
track *inside* a context" into one request type, because the difference is only
in which fields you set:

```go
// A single track, detached.
client.Play(ctx, userID, spotify.PlayRequest{URI: track.URI})

// A whole album/playlist/artist (a bare context URI plays the whole thing).
client.Play(ctx, userID, spotify.PlayRequest{URI: "spotify:playlist:37i9dQ..."})

// A track *within* a playlist, so skip next/previous stay bounded to it.
client.Play(ctx, userID, spotify.PlayRequest{
    ContextURI: "spotify:playlist:37i9dQ...",
    URI:        track.URI, // where to start inside the context
})

// The zero value resumes whatever is already loaded.
client.Play(ctx, userID, spotify.PlayRequest{})
```

`TransferPlayback` is the neighbour: it wakes an available-but-idle device by
moving the *existing* session onto it, rather than starting new content.

## Connecting a user (OAuth)

Before any command runs, the user grants access once. The library provides the
Spotify-facing steps; the HTTP endpoints and user session stay yours.

| Library provides | You build |
| --- | --- |
| `AuthURL(state)` → the Spotify consent link | `GET /spotify/connect` and `/spotify/callback` handlers |
| `Exchange(ctx, code)` → refresh token | Minting and verifying `state` |
| `Connect(ctx, userID, code)` → exchange **and** persist in one step | Knowing which user a callback belongs to |
| `Disconnect` / `Connected` | The "Connect Spotify" button |

**Flow:** your `/connect` handler calls `AuthURL(state)` and redirects the user
there → they approve on Spotify → Spotify redirects to your `/callback?code=…&state=…`
→ you verify `state`, then call `Connect(ctx, userID, code)`. From then on every
capability works until `Disconnect`.

```go
// GET /spotify/connect — user is already logged into your app, so userID is known.
state := signJWT(userID, 5*time.Minute)
http.Redirect(w, r, client.AuthURL(state), http.StatusFound)

// GET /spotify/callback?code=…&state=…
userID, err := verifyJWT(r.URL.Query().Get("state")) // reject if invalid/expired
if err != nil {
    http.Error(w, "bad state", http.StatusBadRequest)
    return
}
err = client.Connect(ctx, userID, r.URL.Query().Get("code"))
var scopeErr *spotify.ScopeError
switch {
case errors.As(err, &scopeErr):
    // User approved but withheld a scope — fail at connect, not months later on a 403.
    // scopeErr.Missing lists them. Send them back through consent; do not swallow this.
    redirectToReconnect(w, r)
case err != nil:
    http.Error(w, "spotify connect failed", http.StatusBadGateway)
}
```

Three details the library takes an explicit position on:

- **`state` should be a short signed JWT (`uid` + `exp`), not a raw `userID`.** A
  raw ID is guessable and leaks in URLs and logs; a JWT is stateless (no
  server-side session table) and, with a ~5-minute expiry, replay is a non-issue
  because Spotify's `code` is already single-use. A JWT is *signed, not
  encrypted* — keep the payload minimal.
- **The redirect URI must be byte-identical at `AuthURL` and `Exchange`** (RFC
  6749 §4.1.3). Set it once on the authenticator for the simple case; when the
  public callback lives behind a gateway you don't own at construction time, pass
  it per call with `spotify.WithRedirectURI(...)` on **both** calls, carrying the
  value in the signed `state`.
- **`Exchange` validates `RequiredScopes` and fails closed.** A user who
  unticks a permission gets a `*ScopeError` at connect time, not a mystery `403`
  during playback weeks later.

## Error sentinels

Every API error routes through one place and, when recognizable, joins a package
sentinel while keeping the original Spotify error in the chain. Match with
`errors.Is` / `errors.As` and branch on intent, not on HTTP status:

| Sentinel | Meaning | Suggested reaction |
| --- | --- | --- |
| `ErrNotConnected` | No stored token (user never connected, or disconnected) | Route into the OAuth flow |
| `*ScopeError` (wraps `ErrMissingScopes`) | Connected but a required scope is missing | Prompt reconnect; `.Missing` lists them |
| `ErrNoActiveDevice` | Spotify has no device to act on (`404 NO_ACTIVE_DEVICE`) | Ask the user to open Spotify |
| `ErrPremiumRequired` | Playback needs Premium (`403`) | Inform the user |
| `ErrRateLimited` | Throttled (`429`) | Back off and retry |

## Design decisions & trade-offs

Each choice below is stated as what we did, the plausible alternative we passed
on, and why — so a reviewer can see the reasoning rather than reconstruct it.

### 1. `TokenStore` is a consumer-defined interface, not a struct we own

**Choice.** The library defines a three-method `TokenStore` interface (in
`client.go`, next to its consumer) and depends only on that. Concrete Postgres
and Firestore stores ship as separate sub-packages you opt into.

**Alternative.** Ship one blessed storage struct, or take a `*sql.DB` directly.

**Why.** [*"Accept interfaces, return structs."*](https://go.dev/wiki/CodeReviewComments#interfaces)
Defining the interface where it is consumed lets a caller back tokens with
anything — Redis, Vault, an existing users table — without the library growing a
dependency on any of them. The bundled stores are conveniences, not the
contract. A caller who implements `TokenStore` and never imports the sub-packages
pays for no database driver in their build graph.

### 2. The library runs no HTTP server

**Choice.** No listener, no router, no callback handler. The library produces an
`AuthURL` and consumes a `code`; the endpoints that carry them are the caller's.

**Alternative.** Bundle a ready-made `/callback` handler for one-line setup.

**Why.** The OAuth redirect lands on a public address the *consumer* owns, often
behind their own auth, routing, and TLS. A handler baked into the library would
fight that instead of fitting it, and would force a transport opinion on a
library whose entire job is API translation. `WithRedirectURI` exists precisely
so the callback can live behind a gateway the library never knows about. The cost
— the caller writes two small handlers — is documented end-to-end above.

### 3. A fresh token refresh per call, held in no cache

**Choice.** `clientFor` builds a token-refreshing `*http.Client` per request from
the stored refresh token and discards it afterward. No access token or client is
cached between calls.

**Alternative.** Cache `*spotify.Client` (or the access token + expiry) per user.

**Why.** For the target traffic — sporadic, one action per intent — a cache adds
per-user lifecycle, expiry, and invalidation state to save a refresh round-trip
that rarely repeats inside a token's lifetime. The stateless path is *correct by
construction*: every call reads the current stored token, so a `Disconnect` or a
re-`Connect` takes effect immediately with no stale client to evict. This is the
decision most tied to the stated traffic shape; under sustained per-user load the
balance flips, and the honest place that would change is here.

### 4. Two playlist reads bypass the upstream client

**Choice.** `SearchPlaylists`, `UserPlaylists`, and `PlaylistTracks` issue their
own `net/http` GETs and decode Spotify's response directly (`getJSON` in
`client.go`), instead of calling `zmb3/spotify/v2`. Everything else uses zmb3.

**Alternative.** Route everything through zmb3 for uniformity.

**Why — and pre-empting the "why not just use the client everywhere?" review
note.** Spotify's 2026 API migration renamed a playlist's track collection from
`tracks` to `items` and moved the listing endpoint from `/tracks` to `/items`.
`zmb3/spotify/v2@v2.4.3` still reads the retired shape, so *through the client
every playlist reports as empty*. Rather than fork the dependency, we read the
current shape ourselves for exactly the affected calls and keep zmb3 for the rest.
The non-200 branch of `getJSON` decodes into a `spotify.Error`, so these
hand-rolled calls classify through the *same* `wrapError`/`sentinelFor` path as
every zmb3 call — the bypass is invisible to callers. `TestPlaylistObjectToPlaylist`
pins the post-migration decode so a dependency bump can't silently regress it.

### 5. Sentinels are matched by status, disambiguated by message

**Choice.** `sentinelFor` maps a `spotify.Error` to a package sentinel using the
HTTP status, and for `403`/`404` also inspects the message text (`"premium"`,
`"No active device"`).

**Alternative.** Trust the status code alone.

**Why.** Spotify encodes the precise cause in a `reason` field that zmb3 discards,
and `403`/`404` are each overloaded (`403` = premium *or* insufficient scope;
`404` = no device *or* genuinely missing). Message-sniffing is the only signal
left once `reason` is gone. It is deliberately *fail-open*: an unrecognized
message falls through to no sentinel and surfaces as a plain wrapped error, never
a wrong one. Every mapped and unmapped case is pinned in `TestSentinelFor`.

### 6. Search runs unscoped; playback relinks by market

**Choice.** `SearchTracks` sends no `market`; `CurrentPlayback` sends
`market=from_token`.

**Alternative.** Send `market=from_token` everywhere for region-correct results.

**Why.** `market=from_token` requires the `user-read-private` scope, which is
*outside* `RequiredScopes` — requesting it on search would `403` with
"Insufficient client scope" and force every consumer to grant a broader scope
than the feature set needs. So search stays unscoped and returns
globally-available results (a rare region-locked hit would fail at `Play`, which
reports it cleanly), while playback state — where relinking actually matters and
the token already carries the market — uses it. The scope surface stays minimal;
the [at-a-glance table](#at-a-glance) shows search needs no scope at all.

### 7. Fixed result limits, no pagination

**Choice.** Search returns 10, `UserPlaylists` 20, `PlaylistTracks` 50 — fixed,
no cursor API.

**Alternative.** Expose `limit`/`offset` or an iterator.

**Why.** One action per agent intent needs a first page, not exhaustive
enumeration; a pageable API would add cursor state and surface area that no
current caller exercises. This is a scope boundary, not an oversight — it is
listed under [non-goals](#non-goals) so the ceiling is explicit, and it is a
small, additive change if a real paging need appears.

## Design principles

Cross-cutting rules the code holds to, so the decisions above read as a system
rather than one-offs:

- **Standard library first.** Direct Web API calls use `net/http` + `encoding/json`;
  no HTTP or JSON helper libraries enter the tree for them.
- **Fail closed at the boundary.** Missing scopes are rejected at `Exchange`;
  invalid Firestore document IDs are rejected before the write, loudly, in
  `validateUserID`.
- **Errors are values, matched with `errors.Is`/`errors.As`.** Callers branch on
  sentinels, never on status codes or string matching; the original error always
  stays in the chain.
- **No global state, no `init()` magic.** Dependencies are injected through `New`;
  the zero-value `PlayRequest` has a defined, documented meaning.

## Non-goals

Deliberately out of scope — naming them is part of keeping the surface honest:

- **No HTTP server or callback handler** — the consumer owns transport ([#2](#2-the-library-runs-no-http-server)).
- **No token encryption** — `TokenStore` is an interface; encryption-at-rest is
  the store implementation's call, not something a library can impose. The
  bundled stores keep the refresh token as given, so a consumer that needs
  envelope encryption wraps its own `TokenStore`.
- **No pagination / configurable limits** ([#7](#7-fixed-result-limits-no-pagination)).
- **No access-token caching / connection pooling to Spotify** ([#3](#3-a-fresh-token-refresh-per-call-held-in-no-cache)).
- **No refresh-token rotation persistence** — only relevant to the PKCE flow;
  Spotify's default Authorization Code flow does not rotate the refresh token.

## Token stores

Both bundled stores satisfy `TokenStore`, key on the opaque `userID`, and map
"no token" to `ErrNotConnected` so the client can route an unconnected user into
OAuth.

**`postgres`** — one `spotify_tokens` table (`owner text` PK, `refresh_token`,
`created_at`). Save is an idempotent upsert (`ON CONFLICT (owner) DO UPDATE`).
Schema is embedded via `//go:embed` and applied with
[`golang-migrate`](https://github.com/golang-migrate/migrate) through
`store.Migrate()`; `New(pool, dsn)` takes the DSN explicitly rather than
reconstructing it from the pool, so `Migrate` uses the exact connection string
you configured.

**`firestore`** — one document per user (doc ID = `userID`, collection
`spotify_tokens`, overridable with `WithCollection`), so lookups are direct
reads, not queries. Save runs in a transaction so `created_at` is written once
and preserved across replacements even under concurrent saves. `userID` is used
verbatim as the document ID, so `validateUserID` rejects the IDs Firestore
forbids (`.`/`..`, `/`, the reserved `__*__` pattern, > 1500 bytes) *before* the
write, turning an opaque server error into a clear client-side one. No
migrations — Firestore is schemaless.

## Compatibility

- **Go:** 1.25+ (module targets `go 1.25`).
- **Direct dependencies:** `zmb3/spotify/v2`, `golang.org/x/oauth2`, and — only
  when you import the matching store — `pgx/v5` + `golang-migrate` or the Cloud
  Firestore SDK.
- **Spotify Web API:** tracks the post-2026-migration playlist shapes (see
  [#4](#4-two-playlist-reads-bypass-the-upstream-client)).

## Verification

Everything below was run in this repository:

```sh
go vet ./...          # clean
gofmt -l .            # no output — all files formatted
go test ./... -cover
```

```
ok   go.naturallyfunny.dev/spotify            coverage: 30.2% of statements
ok   go.naturallyfunny.dev/spotify/firestore  coverage: 14.0% of statements
ok   go.naturallyfunny.dev/spotify/postgres   coverage:  0.0% of statements
```

The headline percentage is low **by design, and worth reading honestly**: this
library is mostly thin translation over live Spotify and database endpoints, and
those wrappers are not unit-tested against a fake network. What *is* tested is
every piece of logic that can be wrong without a network — and that part is at or
near 100%:

| Unit under test | Coverage | What it pins |
| --- | --- | --- |
| `missingScopes`, `grantedScopes` | 100% | Scope diffing and parsing the OAuth `scope` field |
| `sentinelFor`, `wrapError` | 100% | Status→sentinel mapping, including fail-open cases |
| `trackFrom`, `toTrack`, `toPlaylist` | 100% | Decoding the post-migration playlist/track shapes |
| `isContextURI` | 100% | Album/artist/playlist vs track URI classification |
| `Connect`, `Disconnect`, `Connected` | 100% | OAuth persistence wiring (mocked store + token endpoint) |
| `validateUserID` (firestore) | 100% | Every Firestore document-ID constraint |

`Exchange` sits at 75% — the success and scope-rejection paths are covered via a
scripted token endpoint; the raw network-failure branch is not. The `postgres`
store shows 0% because every method is a single SQL round-trip with no branching
logic to unit-test; it is exercised against a real Postgres, not a mock.

## Development

```sh
go test ./...     # unit tests, no network or DB required
go vet ./...
gofmt -l .        # expect no output
```

Contributions are welcome. Match the existing conventions: [Conventional
Commits](https://www.conventionalcommits.org/) for messages (`feat:`, `fix:`,
`refactor:`, `chore(migrate):`), and never edit a migration that is already
committed — add a new one.

## Layout

```
client.go       Client, TokenStore interface, typed results, OAuth lifecycle,
                and the shared error path (wrapError / sentinelFor / getJSON)
search.go       SearchTracks, SearchPlaylists, UserPlaylists, PlaylistTracks
player.go       Devices, CurrentPlayback, Play, TransferPlayback, Pause, Resume,
                Next, Previous, Seek, SetVolume + playback-only sentinels
postgres/       TokenStore over pgxpool; embedded golang-migrate migrations
firestore/      TokenStore over Cloud Firestore; one document per user
```

## License

[MIT](LICENSE) © 2026 Ardian
