package subsonic

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/media"
)

const (
	apiVersion = "1.16.1"
	clientName = "bitchord-selfhosted-addon"
	albumPage  = 500 // getAlbumList2 page size
)

// Options configures the client. Username and Password come from the stored
// "user:password" credential; Navidrome's Subsonic API has no static API key.
type Options struct {
	BaseURL       string
	Username      string
	Password      string
	HeaderTimeout time.Duration
	CallTimeout   time.Duration
}

type Client struct {
	http     *media.Client
	username string
	password string
}

// SplitCredential splits the stored credential "user:password"; the first
// colon separates the two so passwords may contain colons of their own.
func SplitCredential(credential string) (username, password string) {
	username, password, _ = strings.Cut(credential, ":")
	return username, password
}

// JoinCredential builds the stored credential from a username and password.
func JoinCredential(username, password string) string {
	return username + ":" + password
}

func New(o Options) *Client {
	return &Client{
		http: media.NewClient(media.ClientOptions{
			BaseURL:       strings.TrimRight(o.BaseURL, "/"),
			Name:          "navidrome",
			TokenVar:      "the Navidrome username and password",
			HeaderTimeout: o.HeaderTimeout,
			CallTimeout:   o.CallTimeout,
		}),
		username: o.Username,
		password: o.Password,
	}
}

func (c *Client) Name() string { return "Navidrome" }

// authQuery adds Subsonic token authentication: a random salt plus
// md5(password+salt). The password itself never travels on the wire.
func (c *Client) authQuery(q url.Values) url.Values {
	if q == nil {
		q = url.Values{}
	}
	salt := randomSalt()
	sum := md5.Sum([]byte(c.password + salt))
	q.Set("u", c.username)
	q.Set("t", hex.EncodeToString(sum[:]))
	q.Set("s", salt)
	q.Set("v", apiVersion)
	q.Set("c", clientName)
	q.Set("f", "json")
	return q
}

func randomSalt() string {
	raw := make([]byte, 8)
	rand.Read(raw)
	return hex.EncodeToString(raw)
}

// subsonicResponse is the outer envelope every answer shares. The inner body
// is kept raw so it can be decoded into each method's own struct.
type subsonicResponse struct {
	Response json.RawMessage `json:"subsonic-response"`
}

type statusHeader struct {
	Status  string    `json:"status"`
	Version string    `json:"version"`
	Error   *apiError `json:"error"`
}

// call performs a get<method> request, checks the Subsonic status, and decodes
// the inner response body into into.
func (c *Client) call(ctx context.Context, method string, params url.Values, into any) error {
	q := c.authQuery(params)
	path := "/rest/" + method + ".view?" + q.Encode()
	var env subsonicResponse
	if err := c.http.GetJSON(ctx, path, nil, &env); err != nil {
		return err
	}
	var header statusHeader
	if err := json.Unmarshal(env.Response, &header); err != nil {
		return fmt.Errorf("navidrome: malformed answer: %w", err)
	}
	if header.Status != "" && header.Status != "ok" {
		if header.Error != nil {
			return navidromeError(header.Error)
		}
		return fmt.Errorf("navidrome: response status %q", header.Status)
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(env.Response, into); err != nil {
		return fmt.Errorf("navidrome: malformed answer: %w", err)
	}
	return nil
}

func navidromeError(e *apiError) error {
	switch e.Code {
	case 40, 41, 10: // wrong credentials, token auth not supported, required param
		return fmt.Errorf("%w: %s", media.ErrUnauthorized, e.Message)
	case 70: // requested data not found
		return fmt.Errorf("%s: %w", e.Message, media.ErrNotFound)
	}
	return fmt.Errorf("navidrome: %s (code %d)", e.Message, e.Code)
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var answer struct {
		Version string `json:"version"`
	}
	if err := c.call(ctx, "ping", nil, &answer); err != nil {
		return "", err
	}
	return answer.Version, nil
}

func (c *Client) Libraries(ctx context.Context) ([]media.Library, error) {
	var answer struct {
		MusicFolders struct {
			MusicFolder []MusicFolder `json:"musicFolder"`
		} `json:"musicFolders"`
	}
	if err := c.call(ctx, "getMusicFolders", nil, &answer); err != nil {
		return nil, err
	}
	folders := answer.MusicFolders.MusicFolder
	out := make([]media.Library, 0, len(folders))
	for _, folder := range folders {
		out = append(out, media.Library{ID: folder.ID, Name: folder.Name})
	}
	// A single default folder may be omitted; present one so setup lists it.
	if len(out) == 0 {
		out = append(out, media.Library{ID: "", Name: "Music"})
	}
	return out, nil
}

// AllTracks walks getAlbumList2 alphabetically, then lists each album's songs
// with getAlbum. Subsonic has no reliable flat "all songs" paging endpoint, so
// albums are the stable unit.
func (c *Client) AllTracks(ctx context.Context, folderFilter string) ([]media.Track, error) {
	albums, err := c.allAlbums(ctx, folderFilter)
	if err != nil {
		return nil, err
	}
	var out []media.Track
	for _, album := range albums {
		songs, err := c.albumSongs(ctx, album.ID)
		if err != nil {
			return nil, err
		}
		for _, song := range songs {
			out = append(out, song.convert())
		}
	}
	return out, nil
}

func (c *Client) allAlbums(ctx context.Context, folderFilter string) ([]AlbumID3, error) {
	folderID := c.resolveFolderID(ctx, folderFilter)
	var out []AlbumID3
	offset := 0
	for {
		params := url.Values{
			"type":   {"alphabeticalByArtist"},
			"size":   {strconv.Itoa(albumPage)},
			"offset": {strconv.Itoa(offset)},
		}
		if folderID != "" {
			params.Set("musicFolderId", folderID)
		}
		var answer struct {
			AlbumList2 struct {
				Album []AlbumID3 `json:"album"`
			} `json:"albumList2"`
		}
		if err := c.call(ctx, "getAlbumList2", params, &answer); err != nil {
			return nil, err
		}
		page := answer.AlbumList2.Album
		out = append(out, page...)
		if len(page) < albumPage {
			break
		}
		offset += albumPage
	}
	return out, nil
}

func (c *Client) albumSongs(ctx context.Context, albumID string) ([]Child, error) {
	var answer struct {
		Album struct {
			Song []Child `json:"song"`
		} `json:"album"`
	}
	if err := c.call(ctx, "getAlbum", url.Values{"id": {albumID}}, &answer); err != nil {
		return nil, err
	}
	return answer.Album.Song, nil
}

// resolveFolderID turns a library filter (id or name) into a musicFolderId, or
// "" for the whole collection.
func (c *Client) resolveFolderID(ctx context.Context, filter string) string {
	if filter == "" {
		return ""
	}
	if folders, err := c.Libraries(ctx); err == nil {
		for _, folder := range folders {
			if folder.ID == filter || folder.Name == filter {
				return folder.ID
			}
		}
	}
	return filter // best effort: assume the filter is already an id
}

// Track fetches one song by id via getSong.
func (c *Client) Track(ctx context.Context, id string) (media.Track, error) {
	var answer struct {
		Song Child `json:"song"`
	}
	if err := c.call(ctx, "getSong", url.Values{"id": {id}}, &answer); err != nil {
		if isNotFound(err) || isUnauthorized(err) {
			return media.Track{}, media.ErrNotFound
		}
		return media.Track{}, err
	}
	if answer.Song.ID == "" {
		return media.Track{}, media.ErrNotFound
	}
	return answer.Song.convert(), nil
}

func isNotFound(err error) bool     { return errors.Is(err, media.ErrNotFound) }
func isUnauthorized(err error) bool { return errors.Is(err, media.ErrUnauthorized) }

func (c *Client) OpenFile(ctx context.Context, track media.Track, method string, header http.Header) (*http.Response, error) {
	return c.http.Open(ctx, method, c.authedPath(track.FileRef), header)
}

func (c *Client) OpenArt(ctx context.Context, track media.Track) (*http.Response, error) {
	return c.http.Open(ctx, http.MethodGet, c.authedPath(track.ArtRef), nil)
}

// authedPath appends the Subsonic auth query to a stored ref path.
func (c *Client) authedPath(ref string) string {
	if ref == "" {
		return ""
	}
	base, rawQuery, _ := strings.Cut(ref, "?")
	query, _ := url.ParseQuery(rawQuery)
	return base + "?" + c.authQuery(query).Encode()
}
