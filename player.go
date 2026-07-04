package spotify

import (
	"context"
	"errors"
	"strings"

	"github.com/zmb3/spotify/v2"
)

// These errors arise only from playback control, so they live with it. They are
// produced by sentinelFor (see client.go) and matched with errors.Is; the
// original Spotify error remains in the chain.
var (
	// ErrNoActiveDevice means Spotify has no device to act on: the user must
	// open Spotify somewhere before playback commands will land. Spotify
	// reports this as HTTP 404 with reason NO_ACTIVE_DEVICE.
	ErrNoActiveDevice = errors.New("spotify: no active device")

	// ErrPremiumRequired means the action needs a Spotify Premium account.
	// All playback control (Play, Pause, Next, …) is Premium-only; Spotify
	// reports this as HTTP 403.
	ErrPremiumRequired = errors.New("spotify: premium account required")
)

func (c *Client) Devices(ctx context.Context, userID string) ([]Device, error) {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	devices, err := sc.PlayerDevices(ctx)
	if err != nil {
		return nil, wrapError("devices", err)
	}
	result := make([]Device, len(devices))
	for i, d := range devices {
		result[i] = deviceFrom(d)
	}
	return result, nil
}

// CurrentPlayback reports what the user is currently playing, or nil when no
// device is active. Use it to answer "what's playing right now?".
func (c *Client) CurrentPlayback(ctx context.Context, userID string) (*Playback, error) {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Market relinks the returned track to one playable in the user's region.
	state, err := sc.PlayerState(ctx, spotify.Market(spotify.MarketFromToken))
	if err != nil {
		return nil, wrapError("current playback", err)
	}
	// Spotify answers with 204 No Content when nothing is active, which zmb3
	// surfaces as a zero-valued PlayerState; the empty device ID is the tell.
	if state.Device.ID == "" {
		return nil, nil
	}
	pb := &Playback{
		Device:      deviceFrom(state.Device),
		IsPlaying:   state.Playing,
		ProgressMs:  int(state.Progress),
		ContextURI:  string(state.PlaybackContext.URI),
		ContextType: state.PlaybackContext.Type,
	}
	if state.Item != nil {
		track := trackFrom(*state.Item)
		pb.Track = &track
	}
	return pb, nil
}

// PlayRequest describes what to play and where. The zero value resumes whatever
// is already loaded on the active device.
type PlayRequest struct {
	// DeviceID targets a specific device. Empty uses the user's currently active
	// device. A device only has to be available (open somewhere, appearing in
	// Devices) — it need not already be active — for playback to land on it.
	DeviceID string

	// ContextURI is an album, playlist, or artist to play within. When set,
	// playback runs inside that context so skip next/previous stay bounded to
	// it. Empty plays no context.
	ContextURI string

	// URI is a single Spotify entity to play. Its meaning depends on ContextURI:
	//   - With ContextURI set, URI is the track to start at inside the context;
	//     playback begins there and skip next/previous stay in the context.
	//   - Without ContextURI, a track URI plays that track detached, while an
	//     album, playlist, or artist URI plays that whole context (from its
	//     start).
	// Empty starts ContextURI from its beginning, or — with no ContextURI
	// either — resumes whatever is already loaded.
	URI string
}

// Play starts or resumes playback on the user's device according to req. See
// PlayRequest for how DeviceID, ContextURI, and URI combine.
func (c *Client) Play(ctx context.Context, userID string, req PlayRequest) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	opts := &spotify.PlayOptions{}
	if req.DeviceID != "" {
		id := spotify.ID(req.DeviceID)
		opts.DeviceID = &id
	}
	switch {
	case req.ContextURI != "":
		ctxURI := spotify.URI(req.ContextURI)
		opts.PlaybackContext = &ctxURI
		// An offset points playback at a specific track inside the context while
		// keeping skip next/previous bounded to it.
		if req.URI != "" {
			opts.PlaybackOffset = &spotify.PlaybackOffset{URI: spotify.URI(req.URI)}
		}
	case req.URI != "":
		// No explicit context: route a bare context URI (album/playlist/artist)
		// as the context to play whole, and anything else as a single track.
		if isContextURI(req.URI) {
			ctxURI := spotify.URI(req.URI)
			opts.PlaybackContext = &ctxURI
		} else {
			opts.URIs = []spotify.URI{spotify.URI(req.URI)}
		}
	}
	if err := sc.PlayOpt(ctx, opts); err != nil {
		return wrapError("play", err)
	}
	return nil
}

// TransferPlayback moves the active playback session to deviceID, carrying the
// current queue and position across. When play is true it also ensures playback
// is running on the target; when false it preserves the current play/pause
// state. Unlike Play, it continues the existing session rather than starting new
// content, which makes it the clean way to wake an available-but-inactive device
// and keep listening on it. deviceID must name an available device (see Devices).
func (c *Client) TransferPlayback(ctx context.Context, userID, deviceID string, play bool) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	if err := sc.TransferPlayback(ctx, spotify.ID(deviceID), play); err != nil {
		return wrapError("transfer playback", err)
	}
	return nil
}

func (c *Client) Pause(ctx context.Context, userID string) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	if err := sc.Pause(ctx); err != nil {
		return wrapError("pause", err)
	}
	return nil
}

func (c *Client) Resume(ctx context.Context, userID string) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	if err := sc.Play(ctx); err != nil {
		return wrapError("resume", err)
	}
	return nil
}

// Next skips to the next track in the user's queue.
func (c *Client) Next(ctx context.Context, userID string) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	if err := sc.Next(ctx); err != nil {
		return wrapError("next", err)
	}
	return nil
}

// Previous skips to the previous track in the user's queue.
func (c *Client) Previous(ctx context.Context, userID string) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	if err := sc.Previous(ctx); err != nil {
		return wrapError("previous", err)
	}
	return nil
}

// Seek moves the currently playing track to positionMs milliseconds from its
// start. A position past the track's end advances to the next track.
func (c *Client) Seek(ctx context.Context, userID string, positionMs int) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	if err := sc.Seek(ctx, positionMs); err != nil {
		return wrapError("seek", err)
	}
	return nil
}

func (c *Client) SetVolume(ctx context.Context, userID string, percent int) error {
	sc, err := c.clientFor(ctx, userID)
	if err != nil {
		return err
	}
	if err := sc.Volume(ctx, percent); err != nil {
		return wrapError("set volume", err)
	}
	return nil
}

func deviceFrom(d spotify.PlayerDevice) Device {
	return Device{
		ID:       d.ID.String(),
		Name:     d.Name,
		Type:     d.Type,
		IsActive: d.Active,
		Volume:   int(d.Volume),
	}
}

// isContextURI reports whether uri names a Spotify context that playback can be
// pointed at as a whole — an album, artist, or playlist — as opposed to a
// single track. URIs have the form "spotify:<type>:<id>".
func isContextURI(uri string) bool {
	parts := strings.SplitN(uri, ":", 3)
	if len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "album", "artist", "playlist":
		return true
	default:
		return false
	}
}
