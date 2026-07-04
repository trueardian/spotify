package spotify

import (
	"context"
	"fmt"
	"net/url"

	"github.com/zmb3/spotify/v2"
)

func (c *Client) SearchTracks(ctx context.Context, userID, query string) ([]Track, error) {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	// No Market here: market=from_token needs the user-read-private scope, which
	// is outside RequiredScopes, so requesting it 403s with "Insufficient client
	// scope". Search therefore runs unscoped and returns globally-available
	// results; a track surfaced here may occasionally be unplayable in the user's
	// region, which a subsequent Play would report.
	results, err := sc.Search(ctx, query, spotify.SearchTypeTrack, spotify.Limit(10))
	if err != nil {
		return nil, wrapError("search tracks", err)
	}
	tracks := make([]Track, 0, len(results.Tracks.Tracks))
	for _, t := range results.Tracks.Tracks {
		tracks = append(tracks, trackFrom(t))
	}
	return tracks, nil
}

func (c *Client) SearchPlaylists(ctx context.Context, userID, query string) ([]Playlist, error) {
	hc, err := c.httpClientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/search?type=playlist&limit=10&q=%s", apiBase, url.QueryEscape(query))
	var res struct {
		Playlists struct {
			Items []*playlistObject `json:"items"`
		} `json:"playlists"`
	}
	if err := getJSON(ctx, hc, u, &res); err != nil {
		return nil, wrapError("search playlists", err)
	}
	playlists := make([]Playlist, 0, len(res.Playlists.Items))
	for _, p := range res.Playlists.Items {
		if p == nil { // Spotify pads search pages with nulls for filtered results.
			continue
		}
		playlists = append(playlists, p.toPlaylist())
	}
	return playlists, nil
}

func (c *Client) UserPlaylists(ctx context.Context, userID string) ([]Playlist, error) {
	hc, err := c.httpClientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	var page struct {
		Items []*playlistObject `json:"items"`
	}
	if err := getJSON(ctx, hc, apiBase+"/me/playlists?limit=20", &page); err != nil {
		return nil, wrapError("user playlists", err)
	}
	playlists := make([]Playlist, 0, len(page.Items))
	for _, p := range page.Items {
		if p == nil {
			continue
		}
		playlists = append(playlists, p.toPlaylist())
	}
	return playlists, nil
}

func (c *Client) PlaylistTracks(ctx context.Context, userID, playlistID string) ([]Track, error) {
	hc, err := c.httpClientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/playlists/%s/items?limit=50", apiBase, url.PathEscape(playlistID))
	var page struct {
		Items []struct {
			Item *trackObject `json:"item"`
		} `json:"items"`
	}
	if err := getJSON(ctx, hc, u, &page); err != nil {
		return nil, wrapError("playlist tracks", err)
	}
	tracks := make([]Track, 0, len(page.Items))
	for _, it := range page.Items {
		// item is nil for a removed track and non-track for a podcast episode;
		// skip both so a playlist of episodes doesn't surface bogus entries.
		if it.Item == nil || it.Item.Type != "track" {
			continue
		}
		tracks = append(tracks, it.Item.toTrack())
	}
	return tracks, nil
}

func trackFrom(t spotify.FullTrack) Track {
	artists := make([]string, len(t.Artists))
	for i, a := range t.Artists {
		artists[i] = a.Name
	}
	return Track{
		ID:      t.ID.String(),
		Name:    t.Name,
		Artists: artists,
		URI:     string(t.URI),
		URL:     t.ExternalURLs["spotify"],
	}
}

// playlistObject mirrors the fields we read from a Spotify playlist object. Note
// the track summary lives under "items", not "tracks": Spotify's 2026 migration
// renamed it (and moved the listing endpoint from /tracks to /items), which is
// why zmb3 v2.4.3 — still reading "tracks" — reports every playlist as empty.
type playlistObject struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	ExternalURLs map[string]string `json:"external_urls"`
	Items        struct {
		Total int `json:"total"`
	} `json:"items"`
}

func (p playlistObject) toPlaylist() Playlist {
	return Playlist{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Total:       p.Items.Total,
		URL:         p.ExternalURLs["spotify"],
	}
}

// trackObject mirrors the track fields inside a playlist item ("item" after the
// migration). Type distinguishes tracks from podcast episodes in the same list.
type trackObject struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	URI          string            `json:"uri"`
	ExternalURLs map[string]string `json:"external_urls"`
	Artists      []struct {
		Name string `json:"name"`
	} `json:"artists"`
}

func (t trackObject) toTrack() Track {
	artists := make([]string, len(t.Artists))
	for i, a := range t.Artists {
		artists[i] = a.Name
	}
	return Track{
		ID:      t.ID,
		Name:    t.Name,
		Artists: artists,
		URI:     t.URI,
		URL:     t.ExternalURLs["spotify"],
	}
}
