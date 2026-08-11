package invidious

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSelectProgressivePrefersMP4AndUsefulResolution(t *testing.T) {
	got, err := SelectProgressive([]Format{
		{URL: "https://example.test/144", Quality: "144p", Container: "mp4"},
		{URL: "https://example.test/360-webm", Quality: "360p", Container: "webm"},
		{URL: "https://example.test/360", Quality: "360p", Container: "mp4", Itag: "18"},
		{URL: "https://example.test/720", Quality: "720p", Container: "mp4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.test/360" {
		t.Fatalf("selected %q", got.URL)
	}
}

func TestResolveURL(t *testing.T) {
	got, err := ResolveURL("https://inv.example", "/videoplayback?id=1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://inv.example/videoplayback?id=1" {
		t.Fatalf("got %q", got)
	}
}

func TestRejectsHTTPStream(t *testing.T) {
	if _, err := ResolveURL("https://inv.example", "http://unsafe.example/video"); err == nil {
		t.Fatal("expected HTTP URL to be rejected")
	}
}

func TestProviderFailoverSearchAndResolve(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/broken/search":
			http.Error(w, "broken", http.StatusBadGateway)
		case "/working/search":
			fmt.Fprint(w, `<div class="pure-u-1 pure-u-md-1-4"><p class="length">1:02</p><a href="/watch?v=abc123DEF45"><p dir="auto">Test &amp; Video</p></a><p class="channel-name" dir="auto">Author</p><p class="video-data">Shared yesterday</p><p class="video-data">1.2K views</p></div>`)
		case "/working/watch":
			if r.URL.Query().Get("quality") != "dash" {
				http.Error(w, "DASH quality was not requested", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, `<video><source src="/api/manifest/dash/id/abc123DEF45?local=true&amp;unique_res=1" type="application/dash+xml" label="dash"></video>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	broken := server.URL + "/broken"
	working := server.URL + "/working"
	client := New([]string{broken, working})
	client.HTTP = server.Client()
	client.allowPrivateHosts = true
	results, provider, err := client.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if provider != working || client.Current != 1 || len(results) != 1 {
		t.Fatalf("unexpected failover result: provider=%q current=%d results=%d", provider, client.Current, len(results))
	}
	if results[0].Title != "Test & Video" || results[0].Author != "Author" || results[0].LengthSeconds != 62 || results[0].ViewCount != 1200 {
		t.Fatalf("unexpected parsed video: %+v", results[0])
	}
	stream, format, provider, err := client.Resolve(context.Background(), results[0].VideoID)
	if err != nil {
		t.Fatal(err)
	}
	wantStream := server.URL + "/api/manifest/dash/id/abc123DEF45?local=true&unique_res=1"
	if provider != working || stream != wantStream || format.Quality != "DASH" {
		t.Fatalf("unexpected resolve result: provider=%q stream=%q format=%+v", provider, stream, format)
	}
}

func TestTrendingUsesHTMLFeed(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/feed/trending" || r.URL.Query().Get("type") != "Default" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<div class="pure-u-1 pure-u-md-1-4"><p class="length">2:03</p><a href="/watch?v=TREND123456"><p dir="auto">Real trending video</p></a><p class="channel-name" dir="auto">Channel</p><p class="video-data">2.4K views</p></div>`)
	}))
	defer server.Close()
	client := &Client{Providers: []string{server.URL}, HTTP: server.Client(), allowPrivateHosts: true}
	videos, provider, err := client.Trending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider != server.URL || len(videos) != 1 || videos[0].VideoID != "TREND123456" {
		t.Fatalf("unexpected trending feed: provider=%q videos=%+v", provider, videos)
	}
}

func TestSearchAggregatesTwoResultPages(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		videoID := "AAA111bbb22"
		title := "First page"
		if r.URL.Query().Get("page") == "2" {
			videoID = "CCC333ddd44"
			title = "Second page"
		}
		fmt.Fprintf(w, `<div class="pure-u-1 pure-u-md-1-4"><p class="length">1:00</p><a href="/watch?v=%s"><p dir="auto">%s</p></a><p class="channel-name" dir="auto">Channel</p><p class="video-data">1K views</p></div>`, videoID, title)
	}))
	defer server.Close()
	client := &Client{Providers: []string{server.URL}, HTTP: server.Client(), allowPrivateHosts: true}
	videos, _, err := client.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 || videos[0].VideoID != "AAA111bbb22" || videos[1].VideoID != "CCC333ddd44" {
		t.Fatalf("unexpected multi-page search: %+v", videos)
	}
}

func TestStartRelayStreamsThroughClient(t *testing.T) {
	var origin *httptest.Server
	origin = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest":
			w.Header().Set("Content-Type", "application/dash+xml")
			fmt.Fprintf(w, `<MPD><Period><BaseURL>%s/video</BaseURL></Period></MPD>`, origin.URL)
		case "/video":
			if got := r.Header.Get("Range"); got != "bytes=2-" {
				t.Errorf("upstream range = %q", got)
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusPartialContent)
			fmt.Fprint(w, "media")
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	client := &Client{HTTP: origin.Client(), allowPrivateHosts: true}
	relayURL, stop, err := client.StartRelay(origin.URL + "/manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	manifestResponse, err := http.Get(relayURL)
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := io.ReadAll(manifestResponse.Body)
	manifestResponse.Body.Close()
	baseURL := baseURLRE.FindSubmatch(manifest)
	if len(baseURL) < 2 || !strings.HasPrefix(string(baseURL[1]), "http://127.0.0.1:") {
		t.Fatalf("DASH BaseURL was not rewritten: %q", manifest)
	}
	req, _ := http.NewRequest(http.MethodGet, string(baseURL[1]), nil)
	req.Header.Set("Range", "bytes=2-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent || string(body) != "media" {
		t.Fatalf("relay status=%d body=%q", resp.StatusCode, body)
	}
}

func TestResolveDASHTracksSelectsMiyooCompatibleStreams(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("local") != "true" {
			t.Errorf("local proxy query was not preserved: %q", r.URL.RawQuery)
		}
		if r.URL.Query().Get("unique_res") != "1" {
			t.Errorf("unrelated manifest query was lost: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period>
<AdaptationSet mimeType="video/webm"><Representation codecs="vp09" width="854" height="480" bandwidth="500000"><BaseURL>/vp9</BaseURL></Representation></AdaptationSet>
<AdaptationSet mimeType="video/mp4">
<Representation codecs="avc1.4d401e" width="1280" height="720" bandwidth="1200000"><BaseURL>/720</BaseURL></Representation>
<Representation codecs="avc1.4d401e" width="854" height="480" bandwidth="700000"><BaseURL>/480</BaseURL></Representation>
<Representation codecs="avc1.4d401e" width="640" height="360" bandwidth="450000"><BaseURL>/360</BaseURL></Representation>
<Representation codecs="avc1.4d4015" width="426" height="240" bandwidth="250000"><BaseURL>/240</BaseURL></Representation>
</AdaptationSet>
<AdaptationSet mimeType="audio/mp4">
<Representation codecs="mp4a.40.2" bandwidth="64000"><BaseURL>/audio-low</BaseURL></Representation>
<Representation codecs="mp4a.40.2" bandwidth="128000"><BaseURL>/audio</BaseURL></Representation>
<Representation codecs="mp4a.40.2" bandwidth="256000"><BaseURL>/audio-high</BaseURL></Representation>
</AdaptationSet></Period></MPD>`)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), allowPrivateHosts: true}
	tracks, err := client.ResolveDASHTracks(context.Background(), server.URL+"/manifest?local=true&unique_res=1")
	if err != nil {
		t.Fatal(err)
	}
	if tracks.VideoURL != server.URL+"/360" || tracks.AudioURL != server.URL+"/audio" || tracks.Quality != "360p" {
		t.Fatalf("unexpected DASH tracks: %+v", tracks)
	}
	tracks, err = client.ResolveDASHTracksAtMost(context.Background(), server.URL+"/manifest?local=true&unique_res=1", 240)
	if err != nil {
		t.Fatal(err)
	}
	if tracks.VideoURL != server.URL+"/240" || tracks.Quality != "240p" {
		t.Fatalf("unexpected 240p DASH selection: %+v", tracks)
	}
}

func TestResolveDASHTracksInheritsAdaptationAttributes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period>
<AdaptationSet contentType="video" codecs="avc1.4d401e" width="640" height="360">
<Representation bandwidth="450000"><BaseURL>/video</BaseURL></Representation></AdaptationSet>
<AdaptationSet contentType="audio" codecs="mp4a.40.2">
<Representation bandwidth="128000"><BaseURL>/audio</BaseURL></Representation></AdaptationSet>
</Period></MPD>`)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), allowPrivateHosts: true}
	tracks, err := client.ResolveDASHTracks(context.Background(), server.URL+"/manifest")
	if err != nil {
		t.Fatal(err)
	}
	if tracks.VideoURL != server.URL+"/video" || tracks.AudioURL != server.URL+"/audio" || tracks.Quality != "360p" {
		t.Fatalf("unexpected inherited DASH tracks: %+v", tracks)
	}
}

func TestResolveDASHTracksReadsCodecFromMIMEAndHeightFromItag(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period>
<AdaptationSet><Representation id="134" mimeType="video/mp4; codecs=&quot;avc1.4d401e&quot;" bandwidth="450000"><BaseURL>/video</BaseURL></Representation></AdaptationSet>
<AdaptationSet><Representation mimeType="audio/mp4; codecs=&quot;mp4a.40.2&quot;" bandwidth="128000"><BaseURL>/audio</BaseURL></Representation></AdaptationSet>
</Period></MPD>`)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), allowPrivateHosts: true}
	tracks, err := client.ResolveDASHTracks(context.Background(), server.URL+"/manifest")
	if err != nil {
		t.Fatal(err)
	}
	if tracks.VideoURL != server.URL+"/video" || tracks.AudioURL != server.URL+"/audio" || tracks.Quality != "360p" {
		t.Fatalf("unexpected MIME/itag DASH tracks: %+v", tracks)
	}
}

func TestResolveDASHTracksFindsNestedAdaptationSets(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprint(w, `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011"><Period>
<Group><AdaptationSet mimeType="video/mp4"><Representation id="134" codecs="avc1.4d401e" bandwidth="450000"><BaseURL>/video</BaseURL></Representation></AdaptationSet></Group>
<Group><AdaptationSet mimeType="audio/mp4"><Representation id="140" codecs="mp4a.40.2" bandwidth="128000"><BaseURL>/audio</BaseURL></Representation></AdaptationSet></Group>
</Period></MPD>`)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), allowPrivateHosts: true}
	tracks, err := client.ResolveDASHTracks(context.Background(), server.URL+"/manifest")
	if err != nil {
		t.Fatal(err)
	}
	if tracks.VideoURL != server.URL+"/video" || tracks.AudioURL != server.URL+"/audio" || tracks.Quality != "360p" {
		t.Fatalf("unexpected nested DASH tracks: %+v", tracks)
	}
}

func TestRelayNormalizesCompanionRangeStatus(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-" {
			t.Fatalf("upstream range = %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", "bytes 0-7/8")
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("12345678"))
	}))
	defer origin.Close()

	client := &Client{HTTP: origin.Client(), allowPrivateHosts: true}
	relayURL, stop, err := client.StartRelay(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	req, _ := http.NewRequest(http.MethodGet, relayURL, nil)
	req.Header.Set("Range", "bytes=0-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("relay status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 0-7/8" {
		t.Fatalf("relay Content-Range = %q", got)
	}
}

func TestProductionRelayRejectsPrivateNetworkAddress(t *testing.T) {
	client := New(nil)
	if _, _, err := client.StartRelay("https://127.0.0.1/video"); err == nil {
		t.Fatal("private relay target was accepted")
	}
}

func TestRelayRequiresCapabilityToken(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("media"))
	}))
	defer origin.Close()
	client := &Client{HTTP: origin.Client(), allowPrivateHosts: true}
	relayURL, stop, err := client.StartRelay(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	parsed, err := url.Parse(relayURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.RawQuery = ""
	response, err := http.Get(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthenticated relay status = %d, want 404", response.StatusCode)
	}
}

func TestBestThumbnailPrefersMediumUsefulImage(t *testing.T) {
	video := Video{VideoThumbnails: []Thumbnail{
		{URL: "https://example.test/tiny.jpg", Width: 120, Height: 90},
		{URL: "https://example.test/medium.jpg", Width: 480, Height: 360},
		{URL: "https://example.test/huge.jpg", Width: 1920, Height: 1080},
	}}
	got, ok := BestThumbnail(video)
	if !ok || got.URL != "https://example.test/medium.jpg" {
		t.Fatalf("selected %+v, ok=%v", got, ok)
	}
}
