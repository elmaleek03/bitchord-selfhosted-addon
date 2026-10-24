package subsonic

import (
	"net/url"
	"strings"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/media"
)

// The Subsonic API wraps every answer in {"subsonic-response": {...}}. The
// status field is "ok" or "failed"; a failed answer carries an error object.
type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MusicFolder is one library, as getMusicFolders lists them. An empty id means
// the whole collection; Navidrome always has at least the default folder.
type MusicFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AlbumID3 is an album from getAlbumList2.
type AlbumID3 struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Artist    string `json:"artist"`
	ArtistID  string `json:"artistId"`
	CoverArt  string `json:"coverArt"`
	SongCount int    `json:"songCount"`
}

// Child is a song, as getAlbum or search3 returns it.
type Child struct {
	ID          string `json:"id"`
	Parent      string `json:"parent"`
	Title       string `json:"title"`
	Album       string `json:"album"`
	Artist      string `json:"artist"`
	Track       int    `json:"track"`
	Duration    int    `json:"duration"` // seconds
	BitRate     int    `json:"bitRate"`  // kbps
	Suffix      string `json:"suffix"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	CoverArt    string `json:"coverArt"`
	DiscNumber  int    `json:"discNumber"`
	Type        string `json:"type"`
	IsDir       bool   `json:"isDir"`
}

// convert turns a Subsonic song into the addon's common Track. Codec comes
// from the file suffix so the client can decide whether it is lossless.
func (c Child) convert() media.Track {
	out := media.Track{
		ID:          c.ID,
		Title:       c.Title,
		Artist:      c.Artist,
		AlbumArtist: c.Artist,
		Album:       c.Album,
		DurationSec: c.Duration,
		BitrateKbps: c.BitRate,
		Container:   strings.ToLower(c.Suffix),
		Codec:       media.AudioFormat(codecOf(c.Suffix, c.ContentType), strings.ToLower(c.Suffix)),
	}
	if c.CoverArt != "" {
		out.ArtRef = ArtPath(c.CoverArt)
	}
	// A song with no id or no suffix has no playable file.
	if c.ID != "" && c.Suffix != "" && !c.IsDir {
		out.FileRef = StreamPath(c.ID)
	}
	return out
}

// codecOf maps a file suffix to the codec name the rest of the addon uses,
// so Lossless() and the playable-source preference behave as for other servers.
func codecOf(suffix, contentType string) string {
	switch strings.ToLower(suffix) {
	case "flac":
		return "flac"
	case "alac":
		return "alac"
	case "m4a":
		// Most .m4a from a server is AAC; only label ALAC when the server says
		// so, since the difference changes whether a track counts as lossless.
		if strings.Contains(strings.ToLower(contentType), "alac") {
			return "alac"
		}
		return "aac"
	case "mp3":
		return "mp3"
	case "aac":
		return "aac"
	case "opus":
		return "opus"
	case "ogg":
		return "vorbis"
	case "wav":
		return "pcm"
	case "aiff", "aif":
		return "pcm"
	case "wma":
		return "wma"
	case "":
		// Fall back to the MIME subtype, e.g. "audio/flac".
		if i := strings.Index(contentType, "/"); i >= 0 {
			return strings.ToLower(contentType[i+1:])
		}
		return ""
	default:
		return strings.ToLower(suffix)
	}
}

// StreamPath is the Subsonic endpoint that streams the original file, without
// re-encoding, with support for range requests so seeking works.
func StreamPath(id string) string {
	return "/rest/stream.view?id=" + url.QueryEscape(id) + "&format=raw"
}

// ArtPath is the cover-art endpoint, square-cropped to a size BitChord uses.
func ArtPath(id string) string {
	return "/rest/getCoverArt.view?id=" + url.QueryEscape(id) + "&size=600"
}
