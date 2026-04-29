package awareness

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// mediaPlayerContext is the JSON structure for media_player entities.
// State-aware: when the player is actively presenting content
// (playing, paused, buffering) the media metadata is included; when
// idle or off it is omitted because those fields would describe
// stale content. Volume is always meaningful and is normalized from
// HA's 0.0-1.0 range to a 0-100 integer for easier model reasoning.
type mediaPlayerContext struct {
	Entity       string `json:"entity"`
	Name         string `json:"name,omitempty"`
	State        string `json:"state"`
	Volume       any    `json:"volume,omitempty"`
	Muted        bool   `json:"muted,omitempty"`
	Source       string `json:"source,omitempty"`
	AppName      string `json:"app_name,omitempty"`
	MediaTitle   string `json:"media_title,omitempty"`
	MediaArtist  string `json:"media_artist,omitempty"`
	MediaAlbum   string `json:"media_album,omitempty"`
	MediaSeries  string `json:"media_series,omitempty"`
	MediaSeason  any    `json:"media_season,omitempty"`
	MediaEpisode any    `json:"media_episode,omitempty"`
	Since        string `json:"since"`
}

func formatMediaPlayer(state *homeassistant.State, now time.Time) string {
	mc := mediaPlayerContext{
		Entity:  state.EntityID,
		State:   state.State,
		Volume:  normalizeVolume(state.Attributes["volume_level"]),
		Muted:   attrBool(state.Attributes, "is_volume_muted"),
		Source:  attrString(state.Attributes, "source"),
		AppName: attrString(state.Attributes, "app_name"),
		Since:   promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		mc.Name = name
	}
	if hasActiveMedia(state.State) {
		mc.MediaTitle = attrString(state.Attributes, "media_title")
		mc.MediaArtist = attrString(state.Attributes, "media_artist")
		mc.MediaAlbum = attrString(state.Attributes, "media_album_name")
		mc.MediaSeries = attrString(state.Attributes, "media_series_title")
		mc.MediaSeason = state.Attributes["media_season"]
		mc.MediaEpisode = state.Attributes["media_episode"]
	}
	return promptfmt.MarshalCompact(mc)
}

func hasActiveMedia(state string) bool {
	switch state {
	case "playing", "paused", "buffering":
		return true
	}
	return false
}

// normalizeVolume converts HA's 0.0-1.0 volume_level to a 0-100
// integer percentage. Returns nil for nil input so omitempty drops
// the field on devices that don't report volume.
func normalizeVolume(v any) any {
	if v == nil {
		return nil
	}
	switch n := v.(type) {
	case float64:
		return int(n * 100)
	case int:
		return n
	}
	return v
}
