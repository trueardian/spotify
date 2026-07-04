package spotify

import (
	"context"

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
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	results, err := sc.Search(ctx, query, spotify.SearchTypePlaylist, spotify.Limit(10))
	if err != nil {
		return nil, wrapError("search playlists", err)
	}
	playlists := make([]Playlist, 0, len(results.Playlists.Playlists))
	for _, p := range results.Playlists.Playlists {
		playlists = append(playlists, playlistFrom(p))
	}
	return playlists, nil
}

func (c *Client) UserPlaylists(ctx context.Context, userID string) ([]Playlist, error) {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	page, err := sc.CurrentUsersPlaylists(ctx, spotify.Limit(20))
	if err != nil {
		return nil, wrapError("user playlists", err)
	}
	playlists := make([]Playlist, 0, len(page.Playlists))
	for _, p := range page.Playlists {
		playlists = append(playlists, playlistFrom(p))
	}
	return playlists, nil
}

func (c *Client) PlaylistTracks(ctx context.Context, userID, playlistID string) ([]Track, error) {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	page, err := sc.GetPlaylistItems(ctx, spotify.ID(playlistID), spotify.Limit(50))
	if err != nil {
		return nil, wrapError("playlist tracks", err)
	}
	tracks := make([]Track, 0, len(page.Items))
	for _, item := range page.Items {
		if item.Track.Track == nil {
			continue
		}
		tracks = append(tracks, trackFrom(*item.Track.Track))
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

func playlistFrom(p spotify.SimplePlaylist) Playlist {
	return Playlist{
		ID:          p.ID.String(),
		Name:        p.Name,
		Description: p.Description,
		Total:       int(p.Tracks.Total),
		URL:         p.ExternalURLs["spotify"],
	}
}
