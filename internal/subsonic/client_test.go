package subsonic

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rairulyle/bitchord-selfhosted-addon/internal/media"
)

const (
	fakeUser     = "lyle"
	fakePassword = "correct horse"
)

// fakeNavidrome is a minimal Subsonic JSON server: it checks token auth the
// way Navidrome does (md5(password+salt)) and serves the handful of endpoints
// the addon uses.
type fakeNavidrome struct {
	URL    string
	admin  bool // when false, a wrong password is rejected
	server *httptest.Server
}

func newFakeNavidrome(t *testing.T) *fakeNavidrome {
	f := &fakeNavidrome{}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	f.URL = f.server.URL
	return f
}

func (f *fakeNavidrome) serve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if r.URL.Path == "/rest/stream.view" {
		// Streaming is checked before auth in real Navidrome too, but keeps
		// auth to stay honest.
		if !f.authorized(q) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "audio/flac")
		w.Write([]byte("FAKEFILE"))
		return
	}
	if r.URL.Path == "/rest/getCoverArt.view" {
		if !f.authorized(q) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("FAKEART"))
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/rest/") {
		http.NotFound(w, r)
		return
	}
	if !f.authorized(q) {
		writeSub(w, map[string]any{
			"status": "failed",
			"error":  map[string]any{"code": 40, "message": "Wrong username or password."},
		})
		return
	}
	method := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rest/"), ".view")
	switch method {
	case "ping":
		writeSub(w, map[string]any{"status": "ok", "version": "1.16.1"})
	case "getMusicFolders":
		writeSub(w, map[string]any{
			"status": "ok",
			"musicFolders": map[string]any{
				"musicFolder": []map[string]any{{"id": "1", "name": "Music"}},
			},
		})
	case "getAlbumList2":
		offset := atoi(q.Get("offset"))
		albums := []map[string]any{
			{"id": "al-1", "name": "First", "artist": "Artist A"},
			{"id": "al-2", "name": "Second", "artist": "Artist B"},
		}
		// One page only for the test; an offset past the end is empty.
		if offset > 0 {
			albums = nil
		}
		writeSub(w, map[string]any{"status": "ok", "albumList2": map[string]any{"album": albums}})
	case "getAlbum":
		id := q.Get("id")
		songs := map[string][]map[string]any{
			"al-1": {
				{"id": "s-1", "title": "One", "album": "First", "artist": "Artist A", "duration": 120, "suffix": "flac", "bitRate": 900, "coverArt": "al-1"},
				{"id": "s-2", "title": "Two", "album": "First", "artist": "Artist A", "duration": 90, "suffix": "mp3", "bitRate": 320},
			},
			"al-2": {
				{"id": "s-3", "title": "Three", "album": "Second", "artist": "Artist B", "duration": 200, "suffix": "m4a", "contentType": "audio/mp4", "bitRate": 256},
			},
		}[id]
		if songs == nil {
			writeSub(w, map[string]any{"status": "failed", "error": map[string]any{"code": 70, "message": "not found"}})
			return
		}
		writeSub(w, map[string]any{"status": "ok", "album": map[string]any{"song": songs}})
	case "getSong":
		id := q.Get("id")
		if id != "s-1" {
			writeSub(w, map[string]any{"status": "failed", "error": map[string]any{"code": 70, "message": "not found"}})
			return
		}
		writeSub(w, map[string]any{"status": "ok", "song": map[string]any{
			"id": "s-1", "title": "One", "album": "First", "artist": "Artist A", "duration": 120, "suffix": "flac", "bitRate": 900, "coverArt": "al-1",
		}})
	default:
		writeSub(w, map[string]any{"status": "failed", "error": map[string]any{"code": 0, "message": "unknown method"}})
	}
}

func (f *fakeNavidrome) authorized(q map[string][]string) bool {
	get := func(k string) string {
		if v, ok := q[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	if get("u") != fakeUser {
		return false
	}
	salt := get("s")
	want := get("t")
	sum := md5.Sum([]byte(fakePassword + salt))
	return hex.EncodeToString(sum[:]) == want
}

func writeSub(w http.ResponseWriter, inner map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"subsonic-response": inner})
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func newClient(f *fakeNavidrome) *Client {
	return New(Options{BaseURL: f.URL, Username: fakeUser, Password: fakePassword})
}

func TestVersion(t *testing.T) {
	f := newFakeNavidrome(t)
	version, err := newClient(f).Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != "1.16.1" {
		t.Fatalf("version = %q, want 1.16.1", version)
	}
}

func TestWrongPasswordIsUnauthorized(t *testing.T) {
	f := newFakeNavidrome(t)
	_, err := New(Options{BaseURL: f.URL, Username: fakeUser, Password: "nope"}).Version(context.Background())
	if err == nil || !isUnauthorized(err) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
}

func TestLibraries(t *testing.T) {
	f := newFakeNavidrome(t)
	libs, err := newClient(f).Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 1 || libs[0].ID != "1" || libs[0].Name != "Music" {
		t.Fatalf("libraries = %+v", libs)
	}
}

func TestAllTracks(t *testing.T) {
	f := newFakeNavidrome(t)
	tracks, err := newClient(f).AllTracks(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 3 {
		t.Fatalf("got %d tracks, want 3: %+v", len(tracks), tracks)
	}
	byID := map[string]media.Track{}
	for _, tr := range tracks {
		byID[tr.ID] = tr
	}
	flac := byID["s-1"]
	if flac.Title != "One" || flac.Codec != "flac" || !flac.Playable() {
		t.Fatalf("flac track = %+v", flac)
	}
	if !media.Lossless(flac.Codec) {
		t.Fatal("flac should be lossless")
	}
	mp3 := byID["s-2"]
	if mp3.Codec != "mp3" || mp3.ArtRef != "" {
		t.Fatalf("mp3 track = %+v", mp3)
	}
	m4a := byID["s-3"]
	if m4a.Codec != "aac" || media.Lossless(m4a.Codec) {
		t.Fatalf("m4a should map to aac (not lossless): %+v", m4a)
	}
}

func TestTrack(t *testing.T) {
	f := newFakeNavidrome(t)
	tr, err := newClient(f).Track(context.Background(), "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "One" || tr.DurationSec != 120 {
		t.Fatalf("track = %+v", tr)
	}
	if _, err := newClient(f).Track(context.Background(), "missing"); err != media.ErrNotFound {
		t.Fatalf("missing track err = %v, want ErrNotFound", err)
	}
}

func TestOpenFileAndArt(t *testing.T) {
	f := newFakeNavidrome(t)
	c := newClient(f)
	tracks, err := c.AllTracks(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var withArt media.Track
	for _, tr := range tracks {
		if tr.ArtRef != "" {
			withArt = tr
		}
	}
	file, err := c.OpenFile(context.Background(), withArt, http.MethodGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	file.Body.Close()
	if file.StatusCode != http.StatusOK {
		t.Fatalf("file status = %d", file.StatusCode)
	}
	art, err := c.OpenArt(context.Background(), withArt)
	if err != nil {
		t.Fatal(err)
	}
	art.Body.Close()
	if art.StatusCode != http.StatusOK {
		t.Fatalf("art status = %d", art.StatusCode)
	}
}

func TestSplitJoinCredential(t *testing.T) {
	user, pass := SplitCredential("lyle:pa:ss")
	if user != "lyle" || pass != "pa:ss" {
		t.Fatalf("SplitCredential = %q, %q", user, pass)
	}
	if JoinCredential("lyle", "pa:ss") != "lyle:pa:ss" {
		t.Fatal("JoinCredential round trip failed")
	}
}

func TestResolveFolderByName(t *testing.T) {
	f := newFakeNavidrome(t)
	if got := newClient(f).resolveFolderID(context.Background(), "Music"); got != "1" {
		t.Fatalf("resolveFolderID(Music) = %q, want 1", got)
	}
	if got := newClient(f).resolveFolderID(context.Background(), ""); got != "" {
		t.Fatalf("resolveFolderID(empty) = %q, want empty", got)
	}
}
