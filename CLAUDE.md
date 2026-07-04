# spotify

Module Go `go.naturallyfunny.dev/spotify` — reusable public library untuk integrasi Spotify Web API.
Dirancang sebagai interface-based library agar dapat dipakai lintas project, tidak terikat ke satu database atau satu aplikasi.

## Tujuan Pemakaian (baca sebelum audit)

Library ini dipakai sebagai **tool yang dipanggil oleh AI agent**, bukan sebagai backend high-throughput.
Pola traffic-nya: panggilan sporadik, satu aksi per intent user (cari lagu, putar, pause), volume rendah.
Konteks ini menentukan trade-off di bawah. **Saat mengaudit, jangan menilai repo ini dengan standar
service high-throughput** — beberapa "kelemahan" adalah keputusan sadar, bukan bug. Lihat
"Design Decisions" sebelum melaporkan temuan.

## Struktur

```
client.go       Client, TokenStore interface, tipe Track/Playlist/Device/Playback, OAuth (AuthURL/Exchange),
                error mapping lintas-fitur (ErrRateLimited, wrapError, sentinelFor)
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
err = client.Play(ctx, userID, deviceID, trackURI)
```

## Migrations

Pakai `golang-migrate`. Naming: `000N_deskripsi.up.sql` / `000N_deskripsi.down.sql`.
Semua statement wajib pakai `IF NOT EXISTS` / `IF EXISTS`. Jangan pernah edit migration yang sudah di-commit.

## Previous Session

Migrasi penuh dari microservice ke reusable public library:

- Rename module dari `spotify-api` → `go.avagenc.com/spotify` → `go.naturallyfunny.dev/spotify`
- Hapus seluruh layer HTTP: `main.go`, `handlers/`, Lambda artifacts, `api_documentation.md`
- Skema disimpan di satu migrasi `000001_init` (tabel `spotify_tokens`) — `device_id` dan `gmail` tidak relevan dengan tanggung jawab modul ini. Sempat ada `000002_drop_device_id`/`000003_drop_gmail` lalu di-squash ke `000001`.
- Buat `postgres/` package dengan `TokenStore` interface dan `Store` yang mengimplementasinya
- Ganti implementasi HTTP manual ke Spotify dengan `zmb3/spotify/v2`
- `Client` menerima `TokenStore` dan `*spotifyauth.Authenticator` sebagai dependency injection — tidak membangun credential sendiri
- `clientFor` membuat zmb3 client per-request per-user via oauth2 refresh token flow
- `postgres.New` menerima DSN eksplisit agar `Migrate()` tidak rekonstruksi DSN dari `ConnString()` yang fragile

## Design Decisions (sengaja — bukan temuan audit)

Trade-off berikut sudah ditimbang sadar untuk use-case "tool AI agent, traffic rendah".
Jangan dilaporkan sebagai bug; kalau diubah, harus ada alasan baru yang melampaui catatan ini.

- **Token refresh per-request** (`clientFor`, client.go). Tiap panggilan bikin client baru →
  satu refresh ke Spotify lalu dibuang. Aman untuk traffic rendah. Optimasi (cache `*spotify.Client`
  per-user / persist access token + expiry) ditunda sampai ada kebutuhan throughput nyata.
- **Limit hardcoded, tanpa paginasi** (`search.go` Limit 10/20/50, `player.go`). Cukup untuk
  satu aksi per intent agent. Paginasi/limit konfigurabel ditunda sampai dibutuhkan.
- **Refresh token plaintext** (`postgres/store.go`). Enkripsi-at-rest adalah tanggung jawab
  **konsumen**, bukan library — `TokenStore` adalah interface, library tidak bisa & tidak ingin
  memaksakan strategi enkripsi. Bukan kelalaian.
- **Rotasi refresh token tidak dipersist.** Hanya relevan untuk PKCE flow; flow Authorization Code
  default Spotify tidak merotasi. Diterima untuk sekarang.

## Conventions

- `TokenStore` interface didefinisikan di `client.go` — consumer defined interface
- `postgres.New(pool, dsn)` — DSN disimpan di Store untuk kebutuhan Migrate()
- Tidak ada `pkg/` — flat structure
- Commit message pakai conventional commits: `feat:`, `fix:`, `chore(migrate):` dst
