package spotify

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zmb3/spotify/v2"
)

func TestTrackFrom(t *testing.T) {
	full := spotify.FullTrack{
		SimpleTrack: spotify.SimpleTrack{
			ID:   "6rqhFgbbKwnb9MLmUQDhG6",
			Name: "Bohemian Rhapsody",
			Artists: []spotify.SimpleArtist{
				{Name: "Queen"},
				{Name: "Someone Else"},
			},
			URI:          "spotify:track:6rqhFgbbKwnb9MLmUQDhG6",
			ExternalURLs: map[string]string{"spotify": "https://open.spotify.com/track/6rqhFgbbKwnb9MLmUQDhG6"},
		},
	}

	want := Track{
		ID:      "6rqhFgbbKwnb9MLmUQDhG6",
		Name:    "Bohemian Rhapsody",
		Artists: []string{"Queen", "Someone Else"},
		URI:     "spotify:track:6rqhFgbbKwnb9MLmUQDhG6",
		URL:     "https://open.spotify.com/track/6rqhFgbbKwnb9MLmUQDhG6",
	}

	if got := trackFrom(full); !reflect.DeepEqual(got, want) {
		t.Errorf("trackFrom() = %+v, want %+v", got, want)
	}
}

func TestTrackFromNoArtists(t *testing.T) {
	got := trackFrom(spotify.FullTrack{SimpleTrack: spotify.SimpleTrack{ID: "x", Name: "y"}})
	if len(got.Artists) != 0 {
		t.Errorf("Artists = %v, want empty", got.Artists)
	}
}

// TestPlaylistObjectToPlaylist decodes a playlist object in Spotify's post-2026
// shape — the track summary under "items", not "tracks" — and checks the count
// survives the rename. A regression here is exactly what made my_playlists
// report every playlist as empty.
func TestPlaylistObjectToPlaylist(t *testing.T) {
	const body = `{
		"id": "41iitOCfonTIfkRTqqI6Uq",
		"name": "🌌",
		"description": "night drive",
		"external_urls": {"spotify": "https://open.spotify.com/playlist/41iitOCfonTIfkRTqqI6Uq"},
		"items": {"href": "https://api.spotify.com/v1/playlists/41iitOCfonTIfkRTqqI6Uq/items", "total": 11}
	}`

	var p playlistObject
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := Playlist{
		ID:          "41iitOCfonTIfkRTqqI6Uq",
		Name:        "🌌",
		Description: "night drive",
		Total:       11,
		URL:         "https://open.spotify.com/playlist/41iitOCfonTIfkRTqqI6Uq",
	}
	if got := p.toPlaylist(); !reflect.DeepEqual(got, want) {
		t.Errorf("toPlaylist() = %+v, want %+v", got, want)
	}
}

// TestTrackObjectToTrack decodes a playlist item's track, which after the
// migration is wrapped under "item" and carries a "type" that separates tracks
// from podcast episodes.
func TestTrackObjectToTrack(t *testing.T) {
	const body = `{
		"type": "track",
		"id": "6rqhFgbbKwnb9MLmUQDhG6",
		"name": "Bohemian Rhapsody",
		"uri": "spotify:track:6rqhFgbbKwnb9MLmUQDhG6",
		"external_urls": {"spotify": "https://open.spotify.com/track/6rqhFgbbKwnb9MLmUQDhG6"},
		"artists": [{"name": "Queen"}, {"name": "Someone Else"}]
	}`

	var tr trackObject
	if err := json.Unmarshal([]byte(body), &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := Track{
		ID:      "6rqhFgbbKwnb9MLmUQDhG6",
		Name:    "Bohemian Rhapsody",
		Artists: []string{"Queen", "Someone Else"},
		URI:     "spotify:track:6rqhFgbbKwnb9MLmUQDhG6",
		URL:     "https://open.spotify.com/track/6rqhFgbbKwnb9MLmUQDhG6",
	}
	if got := tr.toTrack(); !reflect.DeepEqual(got, want) {
		t.Errorf("toTrack() = %+v, want %+v", got, want)
	}
}
