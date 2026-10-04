# spotify

Module Go `go.trueardian.com/spotify` — reusable public library untuk integrasi Spotify Web API.
Dirancang sebagai interface-based library agar dapat dipakai lintas project, tidak terikat ke satu database atau satu aplikasi.

## Konteks Pemakaian

Library dipakai sebagai **tool yang dipanggil oleh AI agent**: panggilan sporadik, satu aksi per
intent user (cari lagu, putar, pause), volume rendah. Traffic shape ini adalah premis desain yang
disebut eksplisit — beberapa keputusan (refresh per-call, limit tetap) dioptimalkan untuknya, dan
tiap keputusan menunjuk balik ke premis ini sebagai justifikasinya. Rasional lengkap tiap keputusan
(Choice / Alternative / Why) ada di README bagian "Design decisions & trade-offs".

## Struktur

```
client.go       Client, TokenStore interface, tipe Track/Playlist/Device/Playback, OAuth (AuthURL/Exchange/
                Connect/Disconnect), error mapping lintas-fitur (ErrRateLimited, wrapError, sentinelFor)
search.go       SearchTracks, SearchPlaylists, UserPlaylists, PlaylistTracks (tanpa Market: from_token butuh
                scope user-read-private di luar RequiredScopes)
player.go       Devices, CurrentPlayback, Play, Pause, Resume, Next, Previous, Seek, SetVolume,
                sentinel khusus playback (ErrNoActiveDevice, ErrPremiumRequired)
postgres/
  store.go      implementasi TokenStore di atas pgxpool, Migrate()
  migrations/   SQL files, di-embed via //go:embed
firestore/
  store.go      implementasi TokenStore di atas Cloud Firestore: New(client, opts) — satu
                dokumen per user (doc ID = userID, koleksi spotify_tokens, override via
                WithCollection), created_at dipertahankan saat replace (transactional).
                Tanpa migrasi — Firestore schemaless.
```

## Cara Pakai

```go
auth := spotifyauth.New(
    spotifyauth.WithClientID(clientID),
    spotifyauth.WithClientSecret(clientSecret),
)
store := postgres.New(pool, dsn)
client := spotify.New(store, auth)

tracks, err := client.SearchTracks(ctx, userID, "Queen")
err = client.Play(ctx, userID, spotify.PlayRequest{DeviceID: deviceID, URI: trackURI})
```

## Migrations

Pakai `golang-migrate`. Naming: `000N_deskripsi.up.sql` / `000N_deskripsi.down.sql`.
Semua statement wajib pakai `IF NOT EXISTS` / `IF EXISTS`. Jangan pernah edit migration yang sudah di-commit.

## Previous Session

Migrasi penuh dari microservice ke reusable public library:

- Rename module dari `spotify-api` → `go.avagenc.com/spotify` → `go.naturallyfunny.dev/spotify` → `go.trueardian.com/spotify`
- Hapus seluruh layer HTTP: `main.go`, `handlers/`, Lambda artifacts, `api_documentation.md`
- Skema disimpan di satu migrasi `000001_init` (tabel `spotify_tokens`) — `device_id` dan `gmail` tidak relevan dengan tanggung jawab modul ini. Sempat ada `000002_drop_device_id`/`000003_drop_gmail` lalu di-squash ke `000001`.
- Buat `postgres/` package dengan `TokenStore` interface dan `Store` yang mengimplementasinya
- Ganti implementasi HTTP manual ke Spotify dengan `zmb3/spotify/v2`
- `Client` menerima `TokenStore` dan `*spotifyauth.Authenticator` sebagai dependency injection — tidak membangun credential sendiri
- `clientFor` membuat zmb3 client per-request per-user via oauth2 refresh token flow
- `postgres.New` menerima DSN eksplisit agar `Migrate()` tidak rekonstruksi DSN dari `ConnString()` yang fragile

## Design Decisions

Tiap keputusan didokumentasikan lengkap (Choice / Alternative / Why, dengan sitiran otoritas) di
README bagian "Design decisions & trade-offs". Ringkasan sebagai peta:

- **`TokenStore` = consumer-defined interface**, store Postgres/Firestore adalah sub-package opt-in
  (accept interfaces, return structs). README #1.
- **Tanpa HTTP server** — consumer yang punya transport & callback; `WithRedirectURI` untuk callback
  di belakang gateway. README #2.
- **Refresh per-call** (`clientFor`) — stateless & correct-by-construction untuk traffic rendah;
  `Disconnect`/re-`Connect` langsung berlaku tanpa client basi. README #3.
- **Dua playlist read bypass zmb3** via `getJSON` — zmb3 v2.4.3 masih baca shape pra-migrasi 2026
  (`tracks` → `items`), non-200 tetap lewat `wrapError`/`sentinelFor` yang sama. README #4.
- **Sentinel: match by status, disambiguasi by message** untuk 403/404 (fail-open kalau tak dikenal).
  README #5.
- **Search unscoped; playback `market=from_token`** — `market` butuh scope di luar `RequiredScopes`.
  README #6.
- **Limit tetap (10/20/50), tanpa paginasi** — satu aksi per intent butuh first page, bukan enumerasi.
  README #7 / non-goals.
- **Enkripsi token = tanggung jawab implementasi `TokenStore`**, bukan library. README non-goals.
- **Rotasi refresh token tidak dipersist** — hanya relevan untuk PKCE; Authorization Code default tak
  merotasi. README non-goals.

## Conventions

- `TokenStore` interface didefinisikan di `client.go` — consumer defined interface
- `postgres.New(pool, dsn)` — DSN disimpan di Store untuk kebutuhan Migrate()
- Tidak ada `pkg/` — flat structure
- Commit message pakai conventional commits: `feat:`, `fix:`, `chore(migrate):` dst
