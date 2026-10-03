package invidious

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestTLSVerificationToleratesBoundedStaleDeviceClock(t *testing.T) {
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	validFrom := now.Add(25 * 24 * time.Hour)
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "PocketStream test CA"},
		NotBefore: validFrom, NotAfter: validFrom.Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "video.example.test"},
		DNSNames: []string{"video.example.test"}, NotBefore: validFrom,
		NotAfter:    validFrom.Add(90 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, ca}, ServerName: "video.example.test"}
	if err := verifyTLSConnection(state, roots, now); err != nil {
		t.Fatalf("bounded clock skew was rejected: %v", err)
	}
	if err := verifyTLSConnection(state, roots, validFrom.Add(-91*24*time.Hour)); err == nil {
		t.Fatal("clock skew greater than 90 days was accepted")
	}
}

func TestLiveYouTubeResolve(t *testing.T) {
	if os.Getenv("POCKETSTREAM_LIVE_TEST") == "" {
		t.Skip("set POCKETSTREAM_LIVE_TEST=1 to run the network integration test")
	}
	client := New(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	playback, err := client.resolveYouTube(ctx, "oGbSW6Jk0nc", 360)
	if err != nil {
		t.Fatal(err)
	}
	if playback.VideoURL == "" || playback.Quality != "360p" {
		t.Fatalf("unexpected live playback: %+v", playback)
	}
}

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

func TestPipedTrendingFallsBackWhenFeedContainsOnlyLiveStreams(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/trending":
			fmt.Fprint(w, `[{"url":"/watch?v=LIVE1234567","type":"stream","title":"Live","duration":-1}]`)
		case "/search":
			if r.URL.Query().Get("q") != "retro gaming" || r.URL.Query().Get("filter") != "videos" {
				t.Fatalf("unexpected fallback query: %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(pipedSearchResponse{Items: testPipedItems(12)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewWithPiped(nil, []string{server.URL})
	client.HTTP = server.Client()
	client.DirectHTTP = server.Client()
	client.allowPrivateHosts = true
	videos, provider, err := client.Trending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider != server.URL || len(videos) != 12 || videos[0].VideoID != "VID00000000" {
		t.Fatalf("unexpected fallback feed: provider=%q videos=%+v", provider, videos)
	}
}

func TestPipedTrendingFillsSparsePlayableFeed(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/trending":
			fmt.Fprint(w, `[{"url":"/watch?v=TREND123456","type":"stream","title":"Trending","duration":90},{"url":"/watch?v=LIVE1234567","type":"stream","title":"Live","duration":-1}]`)
		case "/search":
			_ = json.NewEncoder(w).Encode(pipedSearchResponse{Items: testPipedItems(12)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewWithPiped(nil, []string{server.URL})
	client.HTTP = server.Client()
	client.DirectHTTP = server.Client()
	client.allowPrivateHosts = true
	videos, _, err := client.Trending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 13 || videos[0].VideoID != "TREND123456" || videos[1].VideoID != "VID00000000" {
		t.Fatalf("sparse feed was not filled: %+v", videos)
	}
}

func TestPipedTrendingRejectsOneCardAndTriesNextProvider(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/sparse/trending":
			fmt.Fprint(w, `[{"url":"/watch?v=TREND123456","type":"stream","title":"Only one","duration":90}]`)
		case "/sparse/search":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/full/trending":
			fmt.Fprint(w, `[]`)
		case "/full/search":
			_ = json.NewEncoder(w).Encode(pipedSearchResponse{Items: testPipedItems(12)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fullProvider := server.URL + "/full"
	client := NewWithPiped(nil, []string{server.URL + "/sparse", fullProvider})
	client.HTTP = server.Client()
	client.DirectHTTP = server.Client()
	client.allowPrivateHosts = true
	videos, provider, err := client.Trending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider != fullProvider || len(videos) != 12 {
		t.Fatalf("provider failover did not fill Home: provider=%q videos=%d", provider, len(videos))
	}
}

func TestPipedHomeContinuationLoadsPageByPage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/trending":
			_ = json.NewEncoder(w).Encode(testPipedItemsFrom(0, 20))
		case "/search":
			_ = json.NewEncoder(w).Encode(pipedSearchResponse{Items: testPipedItemsFrom(20, 20), Nextpage: "more-home"})
		case "/nextpage/search":
			if r.URL.Query().Get("nextpage") != "more-home" || r.URL.Query().Get("q") != "retro gaming" || r.URL.Query().Get("filter") != "videos" {
				t.Fatalf("unexpected Home continuation query: %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode(pipedSearchResponse{Items: testPipedItemsFrom(40, 20)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewWithPiped(nil, []string{server.URL})
	client.HTTP = server.Client()
	client.DirectHTTP = server.Client()
	client.allowPrivateHosts = true
	videos, provider, err := client.Trending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if provider != server.URL || len(videos) != 20 {
		t.Fatalf("initial Home feed: provider=%q videos=%d, want 20", provider, len(videos))
	}
	second, nextpage, err := client.MoreRecommendations(context.Background(), provider, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 20 || second[0].VideoID != "VID00000020" || nextpage != "more-home" {
		t.Fatalf("first continuation: videos=%d first=%q next=%q", len(second), second[0].VideoID, nextpage)
	}
	third, nextpage, err := client.MoreRecommendations(context.Background(), provider, nextpage)
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 20 || third[7].VideoID != "VID00000047" || nextpage != "" {
		t.Fatalf("second continuation: videos=%d item48=%q next=%q", len(third), third[7].VideoID, nextpage)
	}
}

func TestFailedMediaHostCanBeRemovedFromDNSCache(t *testing.T) {
	client := New(nil)
	host := "rr1---sn-test.googlevideo.com"
	client.cacheDNSAddresses(host, []net.IPAddr{{IP: net.ParseIP("142.250.74.110")}})
	if len(client.cachedDNSAddresses(host)) != 1 {
		t.Fatal("test DNS entry was not cached")
	}
	client.forgetDNSAddresses(host)
	if len(client.cachedDNSAddresses(host)) != 0 {
		t.Fatal("failed media DNS entry remained cached")
	}
}

func testPipedItems(count int) []pipedItem {
	return testPipedItemsFrom(0, count)
}

func testPipedItemsFrom(start, count int) []pipedItem {
	items := make([]pipedItem, 0, count)
	for index := 0; index < count; index++ {
		itemIndex := start + index
		items = append(items, pipedItem{
			URL:      fmt.Sprintf("/watch?v=VID%08d", itemIndex),
			Type:     "stream",
			Title:    fmt.Sprintf("Playable %d", itemIndex+1),
			Duration: 120 + itemIndex,
		})
	}
	return items
}

func TestPipedSearchAndQualityAwareResolve(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			fmt.Fprint(w, `{"items":[{"url":"/watch?v=abc123DEF45","type":"stream","title":"Piped result","thumbnail":"https://images.example/thumb.jpg","uploaderName":"Channel","uploadedDate":"today","duration":123,"views":456}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewWithPiped(nil, []string{server.URL})
	client.HTTP = server.Client()
	client.DirectHTTP = server.Client()
	client.allowPrivateHosts = true
	results, provider, err := client.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if provider != server.URL || len(results) != 1 || results[0].VideoID != "abc123DEF45" {
		t.Fatalf("unexpected Piped search: provider=%q results=%+v", provider, results)
	}
	streams := pipedStreamsResponse{
		VideoStreams: []pipedStream{
			{URL: "https://media.example/360.mp4", Format: "MPEG_4", Quality: "360p"},
			{URL: "https://media.example/480.mp4", Format: "MPEG_4", Quality: "480p", VideoOnly: true, Codec: "avc1", Bitrate: 500000},
		},
		AudioStreams: []pipedStream{{URL: "https://media.example/audio.m4a", Format: "MPEG_4", Codec: "mp4a.40.2", Bitrate: 128000}},
	}
	playback, err := selectPipedPlayback(streams, 480)
	if err != nil {
		t.Fatal(err)
	}
	if playback.VideoURL != "https://media.example/360.mp4" || playback.AudioURL != "" || playback.Quality != "360p" {
		t.Fatalf("unexpected progressive playback: %+v", playback)
	}
	playback, err = selectPipedPlayback(streams, 360)
	if err != nil {
		t.Fatal(err)
	}
	if playback.VideoURL != "https://media.example/360.mp4" || playback.AudioURL != "" || playback.Quality != "360p" {
		t.Fatalf("unexpected progressive playback: %+v", playback)
	}
}

func TestPipedSearchLoadsNextPages(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			fmt.Fprint(w, `{"items":[{"url":"/watch?v=AAA111bbb22","type":"stream","title":"First","duration":60}],"nextpage":"page-two"}`)
		case "/nextpage/search":
			if r.URL.Query().Get("nextpage") != "page-two" || r.URL.Query().Get("filter") != "videos" {
				t.Fatalf("unexpected pagination query: %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"items":[{"url":"/watch?v=CCC333ddd44","type":"stream","title":"Second","duration":90}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewWithPiped(nil, []string{server.URL})
	client.HTTP = server.Client()
	client.DirectHTTP = server.Client()
	client.allowPrivateHosts = true
	videos, _, err := client.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(videos) != 2 || videos[0].VideoID != "AAA111bbb22" || videos[1].VideoID != "CCC333ddd44" {
		t.Fatalf("unexpected paginated search: %+v", videos)
	}
}

func TestPipedAndGoogleVideoBypassCompatibilityProxy(t *testing.T) {
	proxied := &http.Client{}
	direct := &http.Client{}
	client := &Client{
		HTTP: proxied, DirectHTTP: direct,
		PipedProviders: []string{"https://api.piped.private.coffee"},
	}
	for _, raw := range []string{
		"https://api.piped.private.coffee/trending",
		"https://proxy.piped.private.coffee/videoplayback?id=abc",
		"https://rr4---sn-test.googlevideo.com/videoplayback?id=abc",
	} {
		endpoint, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := client.httpClientForURL(endpoint); got != direct {
			t.Fatalf("%s did not use direct transport", endpoint.Hostname())
		}
	}
	youtube, _ := url.Parse("https://www.youtube.com/youtubei/v1/player")
	if got := client.httpClientForURL(youtube); got != proxied {
		t.Fatal("YouTube unexpectedly bypassed the compatibility proxy")
	}
}

func TestNewConfiguresEveryCompatibilityRoute(t *testing.T) {
	t.Setenv("POCKETSTREAM_SOCKS5", "127.0.0.1:987, 127.0.0.1:988")
	client := New(nil)
	if len(client.RelayHTTP) != 2 {
		t.Fatalf("relay clients = %d, want 2", len(client.RelayHTTP))
	}
	if client.HTTP != client.RelayHTTP[0] {
		t.Fatal("ordinary requests do not use the first compatibility route")
	}
}

func TestYouTubeRequestFallsThroughCompatibilityRoutes(t *testing.T) {
	failed := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("SOCKS route unavailable")
	})}
	working := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    request,
		}, nil
	})}
	client := &Client{RelayHTTP: []*http.Client{failed, working}, allowPrivateHosts: true}
	var response struct {
		OK bool `json:"ok"`
	}
	if err := client.postJSON(context.Background(), "https://www.youtube.com/test", map[string]string{"test": "value"}, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatal("response from fallback compatibility route was not decoded")
	}
}

func TestFirstRouteReceivesFullAttemptBudget(t *testing.T) {
	slowWorking := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		select {
		case <-time.After(150 * time.Millisecond):
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
				Request:    request,
			}, nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})}
	unavailable := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("route unavailable")
	})}
	client := &Client{RelayHTTP: []*http.Client{slowWorking, unavailable}, DirectHTTP: unavailable, allowPrivateHosts: true}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var response struct {
		OK bool `json:"ok"`
	}
	if err := client.postJSON(ctx, "https://www.youtube.com/test", map[string]string{"test": "value"}, &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatal("slow successful first route was cut off by divided timeout")
	}
}

func TestRelayRetriesThroughCompatibilityProxy(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("media"))
	}))
	defer origin.Close()
	client := &Client{
		HTTP: origin.Client(),
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("direct route failed")
		})},
		PipedProviders:    []string{origin.URL},
		allowPrivateHosts: true,
	}
	relayURL, stop, err := client.StartRelay(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	response, err := http.Get(relayURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusPartialContent || string(body) != "media" {
		t.Fatalf("fallback relay status=%d body=%q", response.StatusCode, body)
	}
}

func TestMediaProxyReusesValidatedPipedAPIDNS(t *testing.T) {
	client := New(nil)
	client.cacheDNSAddresses(pipedAPIHost, []net.IPAddr{{IP: net.ParseIP("1.1.1.1")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	endpoint, err := url.Parse("https://" + pipedMediaProxy + "/videoplayback?id=abc")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.validateRemoteURL(ctx, endpoint); err != nil {
		t.Fatalf("media proxy did not reuse validated API DNS: %v", err)
	}
	addresses := client.cachedDNSAddresses(pipedMediaProxy)
	if len(addresses) != 1 || !addresses[0].IP.Equal(net.ParseIP("1.1.1.1")) {
		t.Fatalf("unexpected cached proxy addresses: %+v", addresses)
	}
}

func TestSecureDNSAcceptsOnlyPublicARecords(t *testing.T) {
	client := &Client{SecureDNSHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "1.1.1.1" || request.URL.Query().Get("name") != "www.youtube.com" || request.Header.Get("Accept") != "application/dns-json" {
			t.Fatalf("unexpected secure DNS request: %s", request.URL)
		}
		body := `{"Status":0,"Answer":[{"type":1,"data":"142.250.74.14"},{"type":1,"data":"127.0.0.1"},{"type":28,"data":"2001:4860:4860::8888"}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}}
	addresses, err := client.lookupSecureDNS(context.Background(), "www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || !addresses[0].IP.Equal(net.ParseIP("142.250.74.14")) {
		t.Fatalf("unexpected secure DNS addresses: %+v", addresses)
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
		if r.Header.Get("Range") == fmt.Sprintf("bytes=0-%d", relayProbeSize-1) {
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Range", "bytes 0-1/8")
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("12"))
			return
		}
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

func TestServeChunkedMediaConvertsOpenRangeToBoundedRequests(t *testing.T) {
	media := make([]byte, relayChunkSize*2+137)
	for index := range media {
		media[index] = byte(index % 251)
	}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, ok := parseByteRange(r.Header.Get("Range"))
		if !ok || end < start {
			http.Error(w, "bounded byte range required", http.StatusBadRequest)
			return
		}
		if start >= int64(len(media)) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if end >= int64(len(media)) {
			end = int64(len(media)) - 1
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(media)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(media[start : end+1])
	}))
	defer origin.Close()

	endpoint, err := url.Parse(origin.URL + "/video")
	if err != nil {
		t.Fatal(err)
	}
	incoming := httptest.NewRequest(http.MethodGet, "http://relay.invalid/stream", nil)
	incoming.Header.Set("Range", "bytes=0-")
	recorder := httptest.NewRecorder()
	handled, err := serveChunkedMedia(recorder, incoming, endpoint, []*http.Client{origin.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("chunked relay did not handle the request")
	}
	response := recorder.Result()
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("relay status = %d, want 206", response.StatusCode)
	}
	if gotRange := response.Header.Get("Content-Range"); gotRange != fmt.Sprintf("bytes 0-%d/%d", len(media)-1, len(media)) {
		t.Fatalf("relay Content-Range = %q", gotRange)
	}
	if !bytes.Equal(got, media) {
		t.Fatalf("relay body length = %d, want %d", len(got), len(media))
	}
}

func TestServeChunkedMediaResumesAfterPartialRouteFailure(t *testing.T) {
	media := make([]byte, relayChunkSize+137)
	for index := range media {
		media[index] = byte(index % 251)
	}
	responseFor := func(request *http.Request, truncate bool) (*http.Response, error) {
		start, end, ok := parseByteRange(request.Header.Get("Range"))
		if !ok || end < start {
			return nil, errors.New("bounded range required")
		}
		if end >= int64(len(media)) {
			end = int64(len(media)) - 1
		}
		payload := media[start : end+1]
		if truncate && len(payload) > 32<<10 {
			payload = payload[:32<<10]
		}
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header: http.Header{
				"Content-Type":  []string{"video/mp4"},
				"Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", start, end, len(media))},
			},
			Body:    io.NopCloser(bytes.NewReader(payload)),
			Request: request,
		}, nil
	}
	flaky := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return responseFor(request, true)
	})}
	stable := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return responseFor(request, false)
	})}
	endpoint, _ := url.Parse("https://media.example.test/video")
	incoming := httptest.NewRequest(http.MethodGet, "http://relay.invalid/stream", nil)
	incoming.Header.Set("Range", "bytes=0-")
	recorder := httptest.NewRecorder()
	handled, err := serveChunkedMedia(recorder, incoming, endpoint, []*http.Client{flaky, stable})
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !bytes.Equal(recorder.Body.Bytes(), media) {
		t.Fatalf("resumed relay body length = %d, want %d", recorder.Body.Len(), len(media))
	}
}

func TestParseByteAndContentRanges(t *testing.T) {
	if start, end, ok := parseByteRange("bytes=17-"); !ok || start != 17 || end != -1 {
		t.Fatalf("open range = (%d, %d, %v)", start, end, ok)
	}
	if _, _, ok := parseByteRange("bytes=-500"); ok {
		t.Fatal("suffix range was accepted")
	}
	if start, end, total, ok := parseContentRange("bytes 17-31/100"); !ok || start != 17 || end != 31 || total != 100 {
		t.Fatalf("content range = (%d, %d, %d, %v)", start, end, total, ok)
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
