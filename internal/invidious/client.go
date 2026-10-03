package invidious

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes  = 2 << 20
	userAgent         = "PocketStream/1.0.21 (OnionOS; Miyoo Mini Plus)"
	youtubePlayerURL  = "https://www.youtube.com/youtubei/v1/player?key=AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8"
	pipedAPIHost      = "api.piped.private.coffee"
	pipedMediaProxy   = "proxy.piped.private.coffee"
	secureDNSEndpoint = "https://1.1.1.1/dns-query"
)

var (
	searchVideoRE = regexp.MustCompile(`(?is)<a\s+href="/watch\?v=([A-Za-z0-9_-]{11})[^"]*"[^>]*>\s*<p[^>]*>(.*?)</p>\s*</a>`)
	cardStart     = []byte(`<div class="pure-u-1 pure-u-md-1-4">`)
	lengthRE      = regexp.MustCompile(`(?is)<p\s+class="length"[^>]*>(.*?)</p>`)
	channelRE     = regexp.MustCompile(`(?is)<p\s+class="channel-name"[^>]*>(.*?)</p>`)
	videoDataRE   = regexp.MustCompile(`(?is)<p\s+class="video-data"[^>]*>(.*?)</p>`)
	sourceTagRE   = regexp.MustCompile(`(?is)<source\b[^>]*>`)
	baseURLRE     = regexp.MustCompile(`(?is)<BaseURL>(.*?)</BaseURL>`)
	htmlTagRE     = regexp.MustCompile(`(?is)<[^>]+>`)
)

type Video struct {
	Type            string      `json:"type"`
	VideoID         string      `json:"videoId"`
	Title           string      `json:"title"`
	Author          string      `json:"author"`
	LengthSeconds   int         `json:"lengthSeconds"`
	ViewCount       int64       `json:"viewCount"`
	PublishedText   string      `json:"publishedText"`
	VideoThumbnails []Thumbnail `json:"videoThumbnails"`
}

type Thumbnail struct {
	Quality string `json:"quality"`
	URL     string `json:"url"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type Format struct {
	URL          string `json:"url"`
	Quality      string `json:"quality"`
	QualityLabel string `json:"qualityLabel"`
	Container    string `json:"container"`
	Type         string `json:"type"`
	Itag         string `json:"itag"`
}

type VideoInfo struct {
	Title         string   `json:"title"`
	FormatStreams []Format `json:"formatStreams"`
}

type DASHTracks struct {
	VideoURL string
	AudioURL string
	Quality  string
}

// Playback is a directly playable progressive stream or a pair of MP4 tracks
// that must be remuxed. Piped currently provides the former for 360p and the
// latter for the remaining quality choices.
type Playback struct {
	VideoURL string
	AudioURL string
	Quality  string
}

type pipedItem struct {
	URL          string `json:"url"`
	Type         string `json:"type"`
	Title        string `json:"title"`
	Thumbnail    string `json:"thumbnail"`
	UploaderName string `json:"uploaderName"`
	UploadedDate string `json:"uploadedDate"`
	Duration     int    `json:"duration"`
	Views        int64  `json:"views"`
}

type pipedSearchResponse struct {
	Items    []pipedItem `json:"items"`
	Nextpage string      `json:"nextpage"`
}

type pipedStream struct {
	URL       string `json:"url"`
	Format    string `json:"format"`
	Quality   string `json:"quality"`
	VideoOnly bool   `json:"videoOnly"`
	Codec     string `json:"codec"`
	Bitrate   int64  `json:"bitrate"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

type pipedStreamsResponse struct {
	Title        string        `json:"title"`
	VideoStreams []pipedStream `json:"videoStreams"`
	AudioStreams []pipedStream `json:"audioStreams"`
}

type youtubeFormat struct {
	URL          string `json:"url"`
	MimeType     string `json:"mimeType"`
	QualityLabel string `json:"qualityLabel"`
	Bitrate      int64  `json:"bitrate"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

type youtubePlayerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	StreamingData struct {
		Formats         []youtubeFormat `json:"formats"`
		AdaptiveFormats []youtubeFormat `json:"adaptiveFormats"`
	} `json:"streamingData"`
}

type dashManifest struct {
	PeriodCount    int
	AdaptationSets []dashAdaptationSet
}

type dashAdaptationSet struct {
	MimeType        string               `xml:"mimeType,attr"`
	ContentType     string               `xml:"contentType,attr"`
	Codecs          string               `xml:"codecs,attr"`
	Width           int                  `xml:"width,attr"`
	Height          int                  `xml:"height,attr"`
	Representations []dashRepresentation `xml:"Representation"`
}

type dashRepresentation struct {
	ID        string `xml:"id,attr"`
	MimeType  string `xml:"mimeType,attr"`
	Codecs    string `xml:"codecs,attr"`
	Width     int    `xml:"width,attr"`
	Height    int    `xml:"height,attr"`
	Bandwidth int64  `xml:"bandwidth,attr"`
	BaseURL   string `xml:"BaseURL"`
}

type Client struct {
	Providers      []string
	Current        int
	PipedProviders []string
	PipedCurrent   int
	HTTP           *http.Client
	DirectHTTP     *http.Client
	RelayHTTP      []*http.Client
	SecureDNSHTTP  *http.Client
	dnsMu          sync.Mutex
	dnsCache       map[string]dnsCacheEntry

	// Unit tests use loopback TLS servers. Production callers cannot enable
	// this field because it is intentionally package-private.
	allowPrivateHosts bool
}

type dnsCacheEntry struct {
	addresses []net.IPAddr
	expires   time.Time
}

func New(providers []string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 20 * time.Second
	roots, rootsErr := x509.SystemCertPool()
	if rootsErr != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	// Miyoo's hardware clock can lag by several weeks after the console has
	// been powered off. Keep full CA, signature, hostname, and expiry checks,
	// but tolerate a certificate whose NotBefore is at most 90 days ahead of
	// that stale local clock. This is deliberately narrower than disabling TLS
	// verification and does not change the console's global time.
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // Verification is performed below with an adjusted time only when necessary.
		VerifyConnection: func(state tls.ConnectionState) error {
			return verifyTLSConnection(state, roots, time.Now())
		},
	}
	client := &Client{
		Providers: append([]string(nil), providers...),
		dnsCache:  make(map[string]dnsCacheEntry),
	}
	directTransport := transport.Clone()
	directTransport.Proxy = nil
	directTransport.DialContext = client.directDialContext
	client.HTTP = &http.Client{
		Transport:     transport,
		Timeout:       25 * time.Second,
		CheckRedirect: client.checkRedirect,
	}
	client.DirectHTTP = &http.Client{
		Transport:     directTransport,
		Timeout:       25 * time.Second,
		CheckRedirect: client.checkRedirect,
	}
	secureDNSTransport := transport.Clone()
	secureDNSTransport.Proxy = nil
	client.SecureDNSHTTP = &http.Client{
		Transport: secureDNSTransport,
		Timeout:   5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for _, proxyAddress := range strings.FieldsFunc(os.Getenv("POCKETSTREAM_SOCKS5"), func(r rune) bool { return r == ',' || r == ';' }) {
		proxyAddress = strings.TrimSpace(proxyAddress)
		if proxyAddress == "" {
			continue
		}
		proxyTransport := transport.Clone()
		proxyTransport.Proxy = nil
		proxyTransport.DialContext = client.socks5DialContext(proxyAddress)
		proxyClient := &http.Client{
			Transport:     proxyTransport,
			Timeout:       25 * time.Second,
			CheckRedirect: client.checkRedirect,
		}
		client.RelayHTTP = append(client.RelayHTTP, proxyClient)
		if len(client.RelayHTTP) == 1 {
			client.HTTP = proxyClient
		}
	}
	return client
}

func verifyTLSConnection(state tls.ConnectionState, roots *x509.CertPool, now time.Time) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("TLS server returned no certificate")
	}
	if state.ServerName == "" {
		return errors.New("TLS server name is empty")
	}
	leaf := state.PeerCertificates[0]
	verifyTime := now
	if now.Before(leaf.NotBefore) {
		skew := leaf.NotBefore.Sub(now)
		if skew > 90*24*time.Hour {
			return fmt.Errorf("system clock is more than 90 days behind certificate validity")
		}
		verifyTime = leaf.NotBefore.Add(time.Minute)
		log.Printf("TLS clock skew tolerated host=%s days=%d", state.ServerName, int(skew.Hours()/24)+1)
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range state.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	_, err := leaf.Verify(x509.VerifyOptions{
		DNSName: state.ServerName, Roots: roots, Intermediates: intermediates,
		CurrentTime: verifyTime, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

// NewWithPiped keeps the legacy Invidious list as a fallback while preferring
// the maintained Piped JSON API for search and stream resolution.
func NewWithPiped(providers, pipedProviders []string) *Client {
	client := New(providers)
	client.PipedProviders = append([]string(nil), pipedProviders...)
	return client
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 4 {
		return errors.New("too many redirects")
	}
	return c.validateRemoteURL(req.Context(), req.URL)
}

func (c *Client) validateRemoteURL(ctx context.Context, endpoint *url.URL) error {
	if endpoint == nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil {
		return errors.New("refusing invalid HTTPS URL")
	}
	if c.allowPrivateHosts {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".home.arpa") {
		return errors.New("refusing local network host")
	}
	if address := net.ParseIP(host); address != nil {
		if !isPublicNetworkIP(address) {
			return errors.New("refusing private network address")
		}
		return nil
	}
	addresses := c.cachedDNSAddresses(host)
	if len(addresses) == 0 && host == pipedMediaProxy {
		// The API and media proxy of this instance intentionally share an
		// origin IP. Reuse the already validated API lookup when the Miyoo's
		// resolver intermittently fails on the proxy subdomain.
		addresses = c.cachedDNSAddresses(pipedAPIHost)
		if len(addresses) > 0 {
			log.Printf("DNS cache reused source=%s target=%s", pipedAPIHost, pipedMediaProxy)
		}
	}
	var err error
	if len(addresses) == 0 {
		if isGoogleServiceHost(host) {
			addresses, err = c.lookupSecureDNS(ctx, host)
			if err == nil && len(addresses) > 0 {
				log.Printf("secure DNS used host=%s", host)
			} else {
				log.Printf("secure DNS failed host=%s", host)
			}
		}
		if len(addresses) == 0 {
			addresses, err = lookupIPAddrResilient(ctx, host)
		}
	}
	if (err != nil || len(addresses) == 0) && host == pipedMediaProxy {
		addresses, err = lookupIPAddrResilient(ctx, pipedAPIHost)
	}
	if err != nil || len(addresses) == 0 {
		return errors.New("upstream hostname did not resolve")
	}
	for _, address := range addresses {
		if !isPublicNetworkIP(address.IP) {
			return errors.New("refusing hostname with a private network address")
		}
	}
	c.cacheDNSAddresses(host, addresses)
	return nil
}

func isGoogleServiceHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") ||
		host == "ytimg.com" || strings.HasSuffix(host, ".ytimg.com") ||
		host == "ggpht.com" || strings.HasSuffix(host, ".ggpht.com") ||
		host == "googlevideo.com" || strings.HasSuffix(host, ".googlevideo.com")
}

func (c *Client) lookupSecureDNS(ctx context.Context, host string) ([]net.IPAddr, error) {
	if c.SecureDNSHTTP == nil {
		return nil, errors.New("secure DNS client unavailable")
	}
	query, err := url.Parse(secureDNSEndpoint)
	if err != nil {
		return nil, err
	}
	values := query.Query()
	values.Set("name", host)
	values.Set("type", "A")
	values.Set("do", "true")
	query.RawQuery = values.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, query.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/dns-json")
	request.Header.Set("User-Agent", userAgent)
	response, err := c.SecureDNSHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("secure DNS HTTP %d", response.StatusCode)
	}
	var payload struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status != 0 {
		return nil, fmt.Errorf("secure DNS status %d", payload.Status)
	}
	addresses := make([]net.IPAddr, 0, len(payload.Answer))
	for _, answer := range payload.Answer {
		if answer.Type != 1 {
			continue
		}
		address := net.ParseIP(strings.TrimSpace(answer.Data))
		if address == nil || !isPublicNetworkIP(address) {
			continue
		}
		addresses = append(addresses, net.IPAddr{IP: address})
	}
	if len(addresses) == 0 {
		return nil, errors.New("secure DNS returned no public IPv4 addresses")
	}
	return addresses, nil
}

func lookupIPAddrResilient(ctx context.Context, host string) ([]net.IPAddr, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		attemptContext, cancel := context.WithTimeout(ctx, 2*time.Second)
		addresses, err := net.DefaultResolver.LookupIPAddr(attemptContext, host)
		cancel()
		if err == nil && len(addresses) > 0 {
			return addresses, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("hostname returned no addresses")
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
	return nil, lastErr
}

func (c *Client) cacheDNSAddresses(host string, addresses []net.IPAddr) {
	if len(addresses) == 0 {
		return
	}
	copyOfAddresses := append([]net.IPAddr(nil), addresses...)
	c.dnsMu.Lock()
	if c.dnsCache == nil {
		c.dnsCache = make(map[string]dnsCacheEntry)
	}
	c.dnsCache[host] = dnsCacheEntry{addresses: copyOfAddresses, expires: time.Now().Add(10 * time.Minute)}
	c.dnsMu.Unlock()
}

func (c *Client) cachedDNSAddresses(host string) []net.IPAddr {
	c.dnsMu.Lock()
	defer c.dnsMu.Unlock()
	entry, ok := c.dnsCache[host]
	if !ok || time.Now().After(entry.expires) {
		if ok {
			delete(c.dnsCache, host)
		}
		return nil
	}
	return append([]net.IPAddr(nil), entry.addresses...)
}

func (c *Client) forgetDNSAddresses(host string) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return
	}
	c.dnsMu.Lock()
	delete(c.dnsCache, host)
	c.dnsMu.Unlock()
}

func (c *Client) directDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 12 * time.Second, KeepAlive: 30 * time.Second}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return dialer.DialContext(ctx, network, address)
	}
	addresses := c.cachedDNSAddresses(strings.TrimSuffix(strings.ToLower(host), "."))
	if len(addresses) == 0 {
		return dialer.DialContext(ctx, network, address)
	}
	var lastErr error
	for _, candidate := range addresses {
		if network == "tcp4" && candidate.IP.To4() == nil {
			continue
		}
		if network == "tcp6" && candidate.IP.To4() != nil {
			continue
		}
		connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("no cached address matches the requested network")
	}
	return nil, lastErr
}

func isPublicNetworkIP(address net.IP) bool {
	if address == nil || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() {
		return false
	}
	if ipv4 := address.To4(); ipv4 != nil {
		// Carrier-grade NAT, benchmarking, and documentation networks are not
		// valid Internet media origins and can expose adjacent infrastructure.
		if ipv4[0] == 100 && ipv4[1]&0xc0 == 64 {
			return false
		}
		if ipv4[0] == 198 && (ipv4[1] == 18 || ipv4[1] == 19) {
			return false
		}
		if (ipv4[0] == 192 && ipv4[1] == 0 && (ipv4[2] == 0 || ipv4[2] == 2)) ||
			(ipv4[0] == 198 && ipv4[1] == 51 && ipv4[2] == 100) ||
			(ipv4[0] == 203 && ipv4[1] == 0 && ipv4[2] == 113) {
			return false
		}
	}
	return true
}

func (c *Client) Search(ctx context.Context, query string) ([]Video, string, error) {
	if len(c.PipedProviders) > 0 {
		if videos, provider, err := c.searchPiped(ctx, query); err == nil {
			return videos, provider, nil
		}
	}
	var videos []Video
	provider, err := c.tryProviders(ctx, func(ctx context.Context, provider string) error {
		// Public Invidious instances currently disable their JSON API. Their
		// normal, JavaScript-free search page contains the same video cards.
		videos = nil
		for page := 1; page <= 2; page++ {
			u := provider + "/search?q=" + url.QueryEscape(query) + "&hl=en-US"
			if page > 1 {
				u += "&page=" + strconv.Itoa(page)
			}
			body, pageErr := c.getHTML(ctx, u)
			if pageErr != nil {
				if page == 1 {
					return pageErr
				}
				break
			}
			pageVideos := parseSearchHTML(body, 24)
			if page == 1 && len(pageVideos) == 0 {
				return errors.New("search page returned no video cards")
			}
			videos = appendUniqueVideos(videos, pageVideos, 48)
			if len(pageVideos) == 0 || len(videos) >= 48 {
				break
			}
		}
		return nil
	})
	return videos, provider, err
}

func appendUniqueVideos(destination, source []Video, limit int) []Video {
	seen := make(map[string]bool, len(destination)+len(source))
	for _, video := range destination {
		seen[video.VideoID] = true
	}
	for _, video := range source {
		if video.VideoID == "" || seen[video.VideoID] {
			continue
		}
		seen[video.VideoID] = true
		destination = append(destination, video)
		if limit > 0 && len(destination) >= limit {
			break
		}
	}
	return destination
}

// Trending returns a non-personalized home feed. Public Invidious instances
// expose it as an ordinary HTML page even when their JSON API is disabled.
func (c *Client) Trending(ctx context.Context) ([]Video, string, error) {
	if len(c.PipedProviders) > 0 {
		if videos, provider, err := c.trendingPiped(ctx); err == nil {
			log.Printf("home feed loaded source=piped count=%d", len(videos))
			return videos, provider, nil
		} else {
			log.Printf("Piped home feed incomplete, trying Invidious: %v", err)
		}
	}
	var videos []Video
	provider, err := c.tryProviders(ctx, func(ctx context.Context, provider string) error {
		body, err := c.getHTML(ctx, provider+"/feed/trending?type=Default&hl=en-US")
		if err != nil {
			return err
		}
		videos = parseSearchHTML(body, 12)
		if len(videos) == 0 {
			return errors.New("trending page returned no video cards")
		}
		return nil
	})
	return videos, provider, err
}

func (c *Client) searchPiped(ctx context.Context, query string) ([]Video, string, error) {
	var videos []Video
	provider, err := c.tryPipedProvidersWithTimeout(ctx, 18*time.Second, func(providerCtx context.Context, provider string) error {
		endpoint := provider + "/search?q=" + url.QueryEscape(query) + "&filter=videos"
		videos = nil
		for page := 0; page < 3 && endpoint != "" && len(videos) < 48; page++ {
			var response pipedSearchResponse
			if err := c.getJSON(providerCtx, endpoint, &response); err != nil {
				if page == 0 {
					return err
				}
				break
			}
			videos = appendUniqueVideos(videos, pipedItemsToVideos(response.Items, 48), 48)
			if strings.TrimSpace(response.Nextpage) == "" {
				break
			}
			endpoint = provider + "/nextpage/search?nextpage=" + url.QueryEscape(response.Nextpage) + "&q=" + url.QueryEscape(query) + "&filter=videos"
		}
		if len(videos) == 0 {
			return errors.New("Piped search returned no videos")
		}
		return nil
	})
	return videos, provider, err
}

func (c *Client) trendingPiped(ctx context.Context) ([]Video, string, error) {
	var videos []Video
	provider, err := c.tryPipedProvidersWithTimeout(ctx, 5*time.Second, func(providerCtx context.Context, provider string) error {
		var response []pipedItem
		if err := c.getJSON(providerCtx, provider+"/trending?region=US", &response); err != nil {
			return err
		}
		videos = pipedItemsToVideos(response, 24)
		if len(videos) < 12 {
			// Public trending feeds can be dominated by live streams, which the
			// Miyoo player does not support. Fill a sparse feed with ordinary,
			// seekable videos instead of leaving Home with only one or two cards.
			var fallback pipedSearchResponse
			endpoint := provider + "/search?q=" + url.QueryEscape("retro gaming") + "&filter=videos"
			if err := c.getJSON(providerCtx, endpoint, &fallback); err != nil {
				return fmt.Errorf("sparse trending feed (%d videos), fallback search failed: %w", len(videos), err)
			} else {
				videos = appendUniqueVideos(videos, pipedItemsToVideos(fallback.Items, 24), 24)
			}
		}
		if len(videos) < 12 {
			return fmt.Errorf("Piped home feed returned only %d playable videos", len(videos))
		}
		return nil
	})
	return videos, provider, err
}

// MoreRecommendations returns the next Home batch. An empty continuation
// starts the discovery search after the fixed /trending batch; every subsequent
// call follows Piped's opaque nextpage token. No app-side item limit is applied.
func (c *Client) MoreRecommendations(ctx context.Context, provider, continuation string) ([]Video, string, error) {
	provider = strings.TrimRight(strings.TrimSpace(provider), "/")
	allowed := false
	for _, configured := range c.PipedProviders {
		if strings.EqualFold(provider, strings.TrimRight(strings.TrimSpace(configured), "/")) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, continuation, errors.New("Home provider does not support continuation")
	}

	query := "retro gaming"
	endpoint := provider + "/search?q=" + url.QueryEscape(query) + "&filter=videos"
	if strings.TrimSpace(continuation) != "" {
		endpoint = provider + "/nextpage/search?nextpage=" + url.QueryEscape(continuation) + "&q=" + url.QueryEscape(query) + "&filter=videos"
	}
	var response pipedSearchResponse
	if err := c.getJSON(ctx, endpoint, &response); err != nil {
		return nil, continuation, err
	}
	return pipedItemsToVideos(response.Items, 0), strings.TrimSpace(response.Nextpage), nil
}

func pipedItemsToVideos(items []pipedItem, limit int) []Video {
	videos := make([]Video, 0, len(items))
	for _, item := range items {
		if item.Type != "stream" || item.Duration <= 0 {
			continue
		}
		parsed, err := url.Parse(item.URL)
		if err != nil {
			continue
		}
		videoID := parsed.Query().Get("v")
		if len(videoID) != 11 || item.Title == "" {
			continue
		}
		video := Video{
			Type: "video", VideoID: videoID, Title: item.Title,
			Author: item.UploaderName, LengthSeconds: item.Duration,
			ViewCount: item.Views, PublishedText: item.UploadedDate,
		}
		if item.Thumbnail != "" {
			video.VideoThumbnails = []Thumbnail{{Quality: "medium", URL: item.Thumbnail, Width: 320, Height: 180}}
		}
		videos = append(videos, video)
		if limit > 0 && len(videos) >= limit {
			break
		}
	}
	return videos
}

func (c *Client) Resolve(ctx context.Context, videoID string) (string, Format, string, error) {
	var chosen Format
	provider, err := c.tryProviders(ctx, func(ctx context.Context, provider string) error {
		u := provider + "/watch?v=" + url.QueryEscape(videoID) + "&quality=dash&hl=en-US"
		body, err := c.getHTML(ctx, u)
		if err != nil {
			return err
		}
		format, err := selectSourceFromHTML(body)
		if err != nil {
			return err
		}
		streamURL, err := ResolveURL(provider, format.URL)
		if err != nil {
			return err
		}
		format.URL = streamURL
		chosen = format
		return nil
	})
	return chosen.URL, chosen, provider, err
}

// ResolvePlaybackAtMost uses YouTube's Android player response first. Public
// Piped /streams endpoints currently return 5xx for ordinary videos, while
// their health checks remain green. Piped and Invidious are retained only as
// fallbacks for installations whose player request is unavailable.
func (c *Client) ResolvePlaybackAtMost(ctx context.Context, videoID string, maxHeight int) (Playback, string, error) {
	if maxHeight <= 0 {
		maxHeight = 360
	}
	if maxHeight > 480 {
		maxHeight = 480
	}
	if playback, err := c.resolveYouTube(ctx, videoID, maxHeight); err == nil {
		return playback, "youtube.com", nil
	} else {
		log.Printf("YouTube player resolve failed, trying Piped: %v", err)
	}
	return c.ResolveFallbackPlaybackAtMost(ctx, videoID, maxHeight)
}

// ResolveYouTubePlaybackAtMost refreshes the signed direct media URL without
// entering the Piped/Invidious fallback chain. A second player request can be
// assigned to another Google Video edge when the first edge is unreachable.
func (c *Client) ResolveYouTubePlaybackAtMost(ctx context.Context, videoID string, maxHeight int) (Playback, error) {
	if maxHeight <= 0 {
		maxHeight = 360
	}
	if maxHeight > 480 {
		maxHeight = 480
	}
	return c.resolveYouTube(ctx, videoID, maxHeight)
}

// ResolveFallbackPlaybackAtMost deliberately skips the direct YouTube player
// response. It is used after a signed googlevideo URL resolves correctly but
// every available network route to that media host fails its byte probe.
func (c *Client) ResolveFallbackPlaybackAtMost(ctx context.Context, videoID string, maxHeight int) (Playback, string, error) {
	if maxHeight <= 0 {
		maxHeight = 360
	}
	if maxHeight > 480 {
		maxHeight = 480
	}
	if len(c.PipedProviders) > 0 {
		var playback Playback
		provider, err := c.tryPipedProviders(ctx, func(providerCtx context.Context, provider string) error {
			var response pipedStreamsResponse
			if err := c.getJSON(providerCtx, provider+"/streams/"+url.PathEscape(videoID), &response); err != nil {
				return err
			}
			selected, err := selectPipedPlayback(response, maxHeight)
			if err != nil {
				return err
			}
			playback = selected
			return nil
		})
		if err == nil {
			return playback, provider, nil
		}
		log.Printf("Piped resolve failed, trying Invidious: %v", err)
	}

	streamURL, format, provider, err := c.Resolve(ctx, videoID)
	if err != nil {
		return Playback{}, "", err
	}
	if format.Container != "dash" {
		return Playback{VideoURL: streamURL, Quality: format.Quality}, provider, nil
	}
	tracks, err := c.ResolveDASHTracksAtMost(ctx, streamURL, maxHeight)
	if err != nil {
		return Playback{}, "", err
	}
	return Playback{VideoURL: tracks.VideoURL, AudioURL: tracks.AudioURL, Quality: tracks.Quality}, provider, nil
}

func (c *Client) resolveYouTube(ctx context.Context, videoID string, maxHeight int) (Playback, error) {
	payload := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": "ANDROID", "clientVersion": "20.10.38",
			"androidSdkVersion": 30, "hl": "en", "gl": "US",
		}},
		"videoId": videoID, "contentCheckOk": true, "racyCheckOk": true,
	}
	var response youtubePlayerResponse
	if err := c.postJSON(ctx, youtubePlayerURL, payload, &response); err != nil {
		return Playback{}, err
	}
	if response.PlayabilityStatus.Status != "OK" {
		reason := strings.TrimSpace(response.PlayabilityStatus.Reason)
		if reason == "" {
			reason = response.PlayabilityStatus.Status
		}
		return Playback{}, fmt.Errorf("YouTube player unavailable: %s", reason)
	}
	converted := pipedStreamsResponse{}
	for _, format := range response.StreamingData.Formats {
		if strings.HasPrefix(strings.ToLower(format.MimeType), "video/mp4") {
			converted.VideoStreams = append(converted.VideoStreams, youtubeToPipedStream(format, false))
		}
	}
	for _, format := range response.StreamingData.AdaptiveFormats {
		lowerMIME := strings.ToLower(format.MimeType)
		switch {
		case strings.HasPrefix(lowerMIME, "video/mp4") && strings.Contains(lowerMIME, "avc"):
			converted.VideoStreams = append(converted.VideoStreams, youtubeToPipedStream(format, true))
		case strings.HasPrefix(lowerMIME, "audio/mp4") && strings.Contains(lowerMIME, "mp4a"):
			converted.AudioStreams = append(converted.AudioStreams, youtubeToPipedStream(format, false))
		}
	}
	return selectPipedPlayback(converted, maxHeight)
}

func youtubeToPipedStream(format youtubeFormat, videoOnly bool) pipedStream {
	codec := ""
	if start := strings.Index(format.MimeType, "codecs=\""); start >= 0 {
		start += len("codecs=\"")
		if end := strings.Index(format.MimeType[start:], "\""); end >= 0 {
			codec = format.MimeType[start : start+end]
		}
	}
	return pipedStream{
		URL: format.URL, Format: "MP4", Quality: format.QualityLabel,
		VideoOnly: videoOnly, Codec: codec, Bitrate: format.Bitrate,
		Width: format.Width, Height: format.Height,
	}
}

func selectPipedPlayback(response pipedStreamsResponse, maxHeight int) (Playback, error) {
	var progressive, progressiveFallback, split, splitFallback pipedStream
	for _, stream := range response.VideoStreams {
		if stream.URL == "" || !isPipedMP4(stream) {
			continue
		}
		height := pipedHeight(stream)
		if height <= 0 || height > 480 {
			continue
		}
		if !stream.VideoOnly {
			if height <= maxHeight && (progressive.URL == "" || height > pipedHeight(progressive)) {
				progressive = stream
			}
			if progressiveFallback.URL == "" || height < pipedHeight(progressiveFallback) {
				progressiveFallback = stream
			}
			continue
		}
		codec := strings.ToLower(stream.Codec)
		if codec != "" && !strings.Contains(codec, "avc") && !strings.Contains(codec, "h264") {
			continue
		}
		if height <= maxHeight && (split.URL == "" || height > pipedHeight(split)) {
			split = stream
		}
		if splitFallback.URL == "" || height < pipedHeight(splitFallback) {
			splitFallback = stream
		}
	}
	// OnionOS ships a very old FFplay.  Give it a progressive MP4 whenever one
	// is available: it decodes that path directly, while a live remux of split
	// tracks can produce working audio with a permanently black video surface.
	selected := progressive
	if selected.URL == "" {
		selected = split
	}
	if selected.URL == "" {
		selected = progressiveFallback
		if splitFallback.URL != "" && (selected.URL == "" || pipedHeight(splitFallback) < pipedHeight(selected)) {
			selected = splitFallback
		}
	}
	if selected.URL == "" {
		return Playback{}, errors.New("Piped returned no MP4 video at 480p or lower")
	}
	if !selected.VideoOnly {
		return Playback{VideoURL: selected.URL, Quality: fmt.Sprintf("%dp", pipedHeight(selected))}, nil
	}
	var audio, audioFallback pipedStream
	for _, stream := range response.AudioStreams {
		if stream.URL == "" || !isPipedMP4(stream) {
			continue
		}
		codec := strings.ToLower(stream.Codec)
		if codec != "" && !strings.Contains(codec, "mp4a") && !strings.Contains(codec, "aac") {
			continue
		}
		if audioFallback.URL == "" || stream.Bitrate < audioFallback.Bitrate {
			audioFallback = stream
		}
		if stream.Bitrate <= 160_000 && (audio.URL == "" || stream.Bitrate > audio.Bitrate) {
			audio = stream
		}
	}
	if audio.URL == "" {
		audio = audioFallback
	}
	if audio.URL == "" {
		return Playback{}, errors.New("Piped returned no AAC audio track")
	}
	return Playback{VideoURL: selected.URL, AudioURL: audio.URL, Quality: fmt.Sprintf("%dp", pipedHeight(selected))}, nil
}

func isPipedMP4(stream pipedStream) bool {
	format := strings.ToLower(strings.TrimSpace(stream.Format))
	return format == "mpeg_4" || format == "mp4" || strings.Contains(format, "mp4")
}

func pipedHeight(stream pipedStream) int {
	if qualityHeight := parseHeight(stream.Quality); qualityHeight > 0 {
		return qualityHeight
	}
	if stream.Height > 0 {
		return stream.Height
	}
	return 0
}

// ResolveDASHTracks converts a DASH manifest into two ordinary MP4 resources
// that the bundled remuxer can pass to OnionOS's player without transcoding.
func (c *Client) ResolveDASHTracks(ctx context.Context, manifestURL string) (DASHTracks, error) {
	return c.ResolveDASHTracksAtMost(ctx, manifestURL, 360)
}

// ResolveDASHTracksAtMost selects the highest H.264 representation at or below
// maxHeight. If that exact range is absent, the manifest's lowest compatible
// representation is used so a video remains playable.
func (c *Client) ResolveDASHTracksAtMost(ctx context.Context, manifestURL string, maxHeight int) (DASHTracks, error) {
	if maxHeight <= 0 {
		maxHeight = 360
	}
	if maxHeight > 480 {
		maxHeight = 480
	}
	parsed, err := url.Parse(manifestURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return DASHTracks{}, errors.New("invalid DASH manifest URL")
	}
	if err := c.validateRemoteURL(ctx, parsed); err != nil {
		return DASHTracks{}, err
	}
	// Keep Invidious' local=true. Original googlevideo URLs are signed for the
	// public instance's egress IP and cannot be fetched directly by the Miyoo.
	// PocketStream's loopback relay still supplies FFmpeg with correct HEAD/Range
	// behaviour while Invidious fetches the IP-bound media upstream.
	headers := make(http.Header)
	headers.Set("Accept", "application/dash+xml,application/xml;q=0.9")
	headers.Set("User-Agent", userAgent)
	resp, err := c.fetchBuffered(ctx, http.MethodGet, parsed.String(), nil, headers, maxResponseBytes)
	if err != nil {
		return DASHTracks{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return DASHTracks{}, fmt.Errorf("DASH manifest HTTP %d", resp.StatusCode)
	}
	manifest, err := parseDASHManifest(resp.Body)
	if err != nil {
		return DASHTracks{}, fmt.Errorf("invalid DASH XML: %w", err)
	}

	var bestVideo, fallbackVideo, unknownBestVideo, unknownFallbackVideo dashRepresentation
	var bestAudio, fallbackAudio, unknownBestAudio, unknownFallbackAudio dashRepresentation
	for _, adaptation := range manifest.AdaptationSets {
		for _, representation := range adaptation.Representations {
			mimeType := representation.MimeType
			if mimeType == "" {
				mimeType = adaptation.MimeType
			}
			contentType := strings.ToLower(adaptation.ContentType)
			codec := representation.Codecs
			if codec == "" {
				codec = adaptation.Codecs
			}
			if codec == "" {
				lowerMIME := strings.ToLower(mimeType)
				if codecIndex := strings.Index(lowerMIME, "codecs="); codecIndex >= 0 {
					codec = lowerMIME[codecIndex:]
				}
			}
			codec = strings.ToLower(codec)
			height := representation.Height
			if height == 0 {
				height = adaptation.Height
			}
			if height == 0 {
				height = dashHeightFromID(representation.ID)
			}
			width := representation.Width
			if width == 0 {
				width = adaptation.Width
			}
			representation.Height = height
			representation.Width = width
			videoMP4 := strings.HasPrefix(strings.ToLower(mimeType), "video/mp4") || (mimeType == "" && contentType == "video")
			audioMP4 := strings.HasPrefix(strings.ToLower(mimeType), "audio/mp4") || (mimeType == "" && contentType == "audio")
			switch {
			case videoMP4 && strings.Contains(codec, "avc") && representation.Height > 0 && representation.Height <= 480:
				if representation.Height <= maxHeight && (bestVideo.BaseURL == "" || representation.Height > bestVideo.Height || (representation.Height == bestVideo.Height && representation.Bandwidth > bestVideo.Bandwidth)) {
					bestVideo = representation
				}
				if fallbackVideo.BaseURL == "" || representation.Height < fallbackVideo.Height {
					fallbackVideo = representation
				}
			case videoMP4 && codec == "" && representation.Height > 0 && representation.Height <= 480:
				if representation.Height <= maxHeight && (unknownBestVideo.BaseURL == "" || representation.Height > unknownBestVideo.Height || (representation.Height == unknownBestVideo.Height && representation.Bandwidth > unknownBestVideo.Bandwidth)) {
					unknownBestVideo = representation
				}
				if unknownFallbackVideo.BaseURL == "" || representation.Height < unknownFallbackVideo.Height {
					unknownFallbackVideo = representation
				}
			case audioMP4 && strings.Contains(codec, "mp4a"):
				// Prefer useful AAC audio without wasting bandwidth on the Miyoo's speaker.
				if fallbackAudio.BaseURL == "" || representation.Bandwidth < fallbackAudio.Bandwidth {
					fallbackAudio = representation
				}
				if representation.Bandwidth <= 160_000 && (bestAudio.BaseURL == "" || representation.Bandwidth > bestAudio.Bandwidth) {
					bestAudio = representation
				}
			case audioMP4 && codec == "":
				if unknownFallbackAudio.BaseURL == "" || representation.Bandwidth < unknownFallbackAudio.Bandwidth {
					unknownFallbackAudio = representation
				}
				if representation.Bandwidth <= 160_000 && (unknownBestAudio.BaseURL == "" || representation.Bandwidth > unknownBestAudio.Bandwidth) {
					unknownBestAudio = representation
				}
			}
		}
	}
	if bestVideo.BaseURL == "" {
		bestVideo = fallbackVideo
	}
	if bestVideo.BaseURL == "" {
		bestVideo = unknownBestVideo
	}
	if bestVideo.BaseURL == "" {
		bestVideo = unknownFallbackVideo
	}
	if bestVideo.BaseURL == "" {
		logDASHInventory(manifest)
		return DASHTracks{}, errors.New("DASH has no H.264 video at 480p or lower")
	}
	if bestAudio.BaseURL == "" {
		bestAudio = fallbackAudio
	}
	if bestAudio.BaseURL == "" {
		bestAudio = unknownBestAudio
	}
	if bestAudio.BaseURL == "" {
		bestAudio = unknownFallbackAudio
	}
	if bestAudio.BaseURL == "" {
		logDASHInventory(manifest)
		return DASHTracks{}, errors.New("DASH has no AAC audio track")
	}
	videoURL, err := resolveDASHBaseURL(resp.URL, bestVideo.BaseURL)
	if err != nil {
		return DASHTracks{}, fmt.Errorf("invalid DASH video URL: %w", err)
	}
	audioURL, err := resolveDASHBaseURL(resp.URL, bestAudio.BaseURL)
	if err != nil {
		return DASHTracks{}, fmt.Errorf("invalid DASH audio URL: %w", err)
	}
	return DASHTracks{VideoURL: videoURL, AudioURL: audioURL, Quality: fmt.Sprintf("%dp", bestVideo.Height)}, nil
}

func dashHeightFromID(id string) int {
	switch strings.TrimSpace(id) {
	case "160":
		return 144
	case "133":
		return 240
	case "134":
		return 360
	case "135":
		return 480
	default:
		return 0
	}
}

// parseDASHManifest intentionally discovers AdaptationSet elements at any
// depth. Most MPDs put them directly below Period, but some Invidious/YouTube
// responses wrap them in additional DASH elements. A rigid struct mapping
// silently discarded every track in those otherwise valid manifests.
func parseDASHManifest(body []byte) (dashManifest, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var manifest dashManifest
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return dashManifest{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "Period":
			manifest.PeriodCount++
		case "AdaptationSet":
			var adaptation dashAdaptationSet
			if err := decoder.DecodeElement(&adaptation, &start); err != nil {
				return dashManifest{}, err
			}
			manifest.AdaptationSets = append(manifest.AdaptationSets, adaptation)
		}
	}
	return manifest, nil
}

func logDASHInventory(manifest dashManifest) {
	log.Printf("DASH inventory periods=%d sets=%d", manifest.PeriodCount, len(manifest.AdaptationSets))
	loggedSets := 0
	loggedRepresentations := 0
	for _, adaptation := range manifest.AdaptationSets {
		if loggedSets >= 8 {
			return
		}
		log.Printf("DASH set content=%q mime=%q codecs=%q reps=%d", adaptation.ContentType, adaptation.MimeType, adaptation.Codecs, len(adaptation.Representations))
		loggedSets++
		for _, representation := range adaptation.Representations {
			if loggedRepresentations >= 16 {
				return
			}
			log.Printf("DASH rep id=%q mime=%q codecs=%q width=%d height=%d bandwidth=%d base=%t", representation.ID, representation.MimeType, representation.Codecs, representation.Width, representation.Height, representation.Bandwidth, representation.BaseURL != "")
			loggedRepresentations++
		}
	}
}

func resolveDASHBaseURL(base *url.URL, value string) (string, error) {
	reference, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != "https" || resolved.Host == "" {
		return "", errors.New("refusing non-HTTPS DASH track")
	}
	return resolved.String(), nil
}

func (c *Client) getHTML(ctx context.Context, endpoint string) ([]byte, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("invalid HTML endpoint")
	}
	if err := c.validateRemoteURL(ctx, parsed); err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "text/html,application/xhtml+xml")
	headers.Set("Accept-Language", "en-US,en;q=0.8")
	headers.Set("User-Agent", userAgent)
	resp, err := c.fetchBuffered(ctx, http.MethodGet, endpoint, nil, headers, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func parseSearchHTML(body []byte, limit int) []Video {
	matches := searchVideoRE.FindAllSubmatchIndex(body, -1)
	if limit <= 0 || limit > len(matches) {
		limit = len(matches)
	}
	videos := make([]Video, 0, limit)
	for index, match := range matches {
		if len(match) < 6 {
			continue
		}
		id := string(body[match[2]:match[3]])
		title := cleanHTML(body[match[4]:match[5]])
		if id == "" || title == "" {
			continue
		}

		start := bytes.LastIndex(body[:match[0]], cardStart)
		if start < 0 {
			start = 0
		}
		end := len(body)
		if index+1 < len(matches) {
			next := bytes.LastIndex(body[:matches[index+1][0]], cardStart)
			if next > match[1] {
				end = next
			} else {
				end = matches[index+1][0]
			}
		}

		before := body[start:match[0]]
		after := body[match[1]:end]
		duration := 0
		if lengths := lengthRE.FindAllSubmatch(before, -1); len(lengths) > 0 {
			duration = parseDurationText(cleanHTML(lengths[len(lengths)-1][1]))
		}
		author := "UNKNOWN CHANNEL"
		if channel := channelRE.FindSubmatch(after); len(channel) > 1 {
			author = cleanHTML(channel[1])
		}
		var views int64
		published := ""
		for _, data := range videoDataRE.FindAllSubmatch(after, -1) {
			text := cleanHTML(data[1])
			if strings.Contains(strings.ToLower(text), "view") {
				views = parseCompactViews(text)
			} else if published == "" {
				published = text
			}
		}

		videos = append(videos, Video{
			Type: "video", VideoID: id, Title: title, Author: author,
			LengthSeconds: duration, ViewCount: views, PublishedText: published,
			VideoThumbnails: []Thumbnail{{
				Quality: "medium", URL: "/vi/" + id + "/mqdefault.jpg", Width: 320, Height: 180,
			}},
		})
		if len(videos) == limit {
			break
		}
	}
	return videos
}

func selectSourceFromHTML(body []byte) (Format, error) {
	var formats []Format
	var dash Format
	for _, tag := range sourceTagRE.FindAll(body, -1) {
		source := htmlAttribute(tag, "src")
		mimeType := htmlAttribute(tag, "type")
		quality := htmlAttribute(tag, "label")
		if source == "" {
			continue
		}
		if strings.EqualFold(mimeType, "application/dash+xml") {
			dash = Format{URL: source, Quality: "DASH", QualityLabel: "DASH", Container: "dash", Type: mimeType}
			continue
		}
		if !strings.HasPrefix(strings.ToLower(mimeType), "video/") {
			continue
		}
		container := strings.TrimPrefix(strings.SplitN(mimeType, ";", 2)[0], "video/")
		itag := ""
		if parsed, err := url.Parse(source); err == nil {
			itag = parsed.Query().Get("itag")
		}
		formats = append(formats, Format{
			URL: source, Quality: quality, QualityLabel: quality,
			Container: container, Type: mimeType, Itag: itag,
		})
	}
	progressive, err := SelectProgressive(formats)
	if err == nil {
		return progressive, nil
	}
	if dash.URL != "" {
		return dash, nil
	}
	return Format{}, err
}

func htmlAttribute(tag []byte, name string) string {
	text := string(tag)
	for _, quote := range []string{`"`, `'`} {
		prefix := name + "=" + quote
		start := strings.Index(text, prefix)
		if start < 0 {
			continue
		}
		start += len(prefix)
		if end := strings.Index(text[start:], quote); end >= 0 {
			return html.UnescapeString(text[start : start+end])
		}
	}
	return ""
}

func cleanHTML(value []byte) string {
	text := htmlTagRE.ReplaceAllString(string(value), " ")
	return strings.Join(strings.Fields(html.UnescapeString(text)), " ")
}

func parseDurationText(value string) int {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	total := 0
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 59 {
			return 0
		}
		total = total*60 + n
	}
	return total
}

func parseCompactViews(value string) int64 {
	value = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(value, ",", ""), " ", ""))
	end := 0
	for end < len(value) && ((value[end] >= '0' && value[end] <= '9') || value[end] == '.') {
		end++
	}
	if end == 0 {
		return 0
	}
	number, err := strconv.ParseFloat(value[:end], 64)
	if err != nil {
		return 0
	}
	multiplier := float64(1)
	if end < len(value) {
		switch value[end] {
		case 'K':
			multiplier = 1_000
		case 'M':
			multiplier = 1_000_000
		case 'B':
			multiplier = 1_000_000_000
		}
	}
	return int64(number * multiplier)
}

func (c *Client) tryProviders(ctx context.Context, fn func(context.Context, string) error) (string, error) {
	if len(c.Providers) == 0 {
		return "", errors.New("no Invidious providers configured")
	}
	var failures []string
	for offset := 0; offset < len(c.Providers); offset++ {
		idx := (c.Current + offset) % len(c.Providers)
		provider := strings.TrimRight(c.Providers[idx], "/")
		if provider == "" {
			continue
		}
		if err := fn(ctx, provider); err == nil {
			c.Current = idx
			return provider, nil
		} else {
			failures = append(failures, fmt.Sprintf("%s: %v", provider, err))
		}
	}
	return "", fmt.Errorf("all providers failed: %s", strings.Join(failures, "; "))
}

func (c *Client) tryPipedProviders(ctx context.Context, fn func(context.Context, string) error) (string, error) {
	return c.tryPipedProvidersWithTimeout(ctx, 7*time.Second, fn)
}

func (c *Client) tryPipedProvidersWithTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context, string) error) (string, error) {
	if len(c.PipedProviders) == 0 {
		return "", errors.New("no Piped providers configured")
	}
	var failures []string
	for offset := 0; offset < len(c.PipedProviders); offset++ {
		idx := (c.PipedCurrent + offset) % len(c.PipedProviders)
		provider := strings.TrimRight(c.PipedProviders[idx], "/")
		if provider == "" {
			continue
		}
		providerCtx, cancel := context.WithTimeout(ctx, timeout)
		err := fn(providerCtx, provider)
		cancel()
		if err == nil {
			c.PipedCurrent = idx
			return provider, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %v", provider, err))
	}
	return "", fmt.Errorf("all Piped providers failed: %s", strings.Join(failures, "; "))
}

type bufferedResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	URL        *url.URL
}

func (c *Client) networkClientsForURL(endpoint *url.URL) []*http.Client {
	clients := make([]*http.Client, 0, len(c.RelayHTTP)+2)
	appendClient := func(candidate *http.Client) {
		if candidate == nil {
			return
		}
		for _, existing := range clients {
			if existing == candidate {
				return
			}
		}
		clients = append(clients, candidate)
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	if isGoogleServiceHost(host) && len(c.RelayHTTP) > 0 {
		for _, compatibilityClient := range c.RelayHTTP {
			appendClient(compatibilityClient)
		}
		appendClient(c.DirectHTTP)
	} else {
		appendClient(c.httpClientForURL(endpoint))
		for _, compatibilityClient := range c.RelayHTTP {
			appendClient(compatibilityClient)
		}
		appendClient(c.DirectHTTP)
	}
	return clients
}

func (c *Client) fetchBuffered(ctx context.Context, method, endpoint string, requestBody []byte, headers http.Header, limit int64) (bufferedResponse, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return bufferedResponse{}, errors.New("invalid HTTPS endpoint")
	}
	if err := c.validateRemoteURL(ctx, parsed); err != nil {
		return bufferedResponse{}, err
	}
	if limit <= 0 {
		limit = maxResponseBytes
	}
	clients := c.networkClientsForURL(parsed)
	if len(clients) == 0 {
		return bufferedResponse{}, errors.New("no HTTP route available")
	}
	var lastErr error
	for route, client := range clients {
		attemptTimeout := 7 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return bufferedResponse{}, ctx.Err()
			}
			if remaining < attemptTimeout {
				attemptTimeout = remaining
			}
		}
		attemptContext, cancel := context.WithTimeout(ctx, attemptTimeout)
		var body io.Reader
		if requestBody != nil {
			body = bytes.NewReader(requestBody)
		}
		request, requestErr := http.NewRequestWithContext(attemptContext, method, endpoint, body)
		if requestErr != nil {
			cancel()
			return bufferedResponse{}, requestErr
		}
		request.Header = headers.Clone()
		response, requestErr := client.Do(request)
		if requestErr != nil {
			cancel()
			lastErr = requestErr
			log.Printf("network route failed host=%s route=%d", parsed.Hostname(), route)
			continue
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		_ = response.Body.Close()
		cancel()
		if readErr != nil {
			lastErr = readErr
			log.Printf("network route read failed host=%s route=%d", parsed.Hostname(), route)
			continue
		}
		if int64(len(responseBody)) > limit {
			return bufferedResponse{}, errors.New("response is too large")
		}
		finalURL := parsed
		if response.Request != nil && response.Request.URL != nil {
			finalURL = response.Request.URL
		}
		return bufferedResponse{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: responseBody, URL: finalURL}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("all HTTP routes failed")
	}
	return bufferedResponse{}, lastErr
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("User-Agent", userAgent)
	resp, err := c.fetchBuffered(ctx, http.MethodGet, endpoint, nil, headers, maxResponseBytes)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func (c *Client) postJSON(ctx context.Context, endpoint string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", "com.google.android.youtube/20.10.38 (Linux; U; Android 11) gzip")
	resp, err := c.fetchBuffered(ctx, http.MethodPost, endpoint, body, headers, maxResponseBytes)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func (c *Client) FetchBytes(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	headers := make(http.Header)
	headers.Set("Accept", "image/jpeg,image/*;q=0.8")
	headers.Set("User-Agent", userAgent)
	resp, err := c.fetchBuffered(ctx, http.MethodGet, endpoint, nil, headers, limit)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// StartRelay exposes one HTTPS media URL on loopback. ffplay does not support
// PocketStream's SOCKS5 transport, so the Go client performs the remote request
// through its network compatibility layer and streams the bytes to ffplay locally. Range requests are
// forwarded because ffplay uses them when seeking and probing MP4 files.
func (c *Client) StartRelay(remote string) (string, func(), error) {
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", nil, errors.New("refusing invalid relay URL")
	}
	validationContext, cancelValidation := context.WithTimeout(context.Background(), 7*time.Second)
	err = c.validateRemoteURL(validationContext, parsed)
	cancelValidation()
	if err != nil {
		return "", nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = listener.Close()
		return "", nil, errors.New("cannot secure local relay")
	}
	token := hex.EncodeToString(tokenBytes)
	primaryHTTP := c.httpClientForURL(parsed)
	clientOrder := make([]*http.Client, 0, len(c.RelayHTTP)+3)
	appendClient := func(candidate *http.Client) {
		if candidate == nil || candidate.Transport == nil {
			return
		}
		for _, existing := range clientOrder {
			if existing == candidate {
				return
			}
		}
		clientOrder = append(clientOrder, candidate)
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if strings.HasSuffix(host, ".googlevideo.com") {
		// On networks that throttle Google Video, try each independently tuned
		// SOCKS compatibility route before spending time on the direct route.
		for _, compatibilityClient := range c.RelayHTTP {
			appendClient(compatibilityClient)
		}
		appendClient(c.DirectHTTP)
	} else {
		appendClient(primaryHTTP)
		for _, compatibilityClient := range c.RelayHTTP {
			appendClient(compatibilityClient)
		}
		appendClient(c.HTTP)
		appendClient(c.DirectHTTP)
	}
	relayClients := make([]*http.Client, 0, len(clientOrder))
	for _, candidate := range clientOrder {
		relayClients = append(relayClients, &http.Client{Transport: candidate.Transport, CheckRedirect: c.checkRedirect})
	}
	log.Printf("relay prepared host=%s routes=%d compatibility=%d", parsed.Hostname(), len(relayClients), len(c.RelayHTTP))
	preferred, err := probeRelayTarget(parsed, relayClients)
	if err != nil {
		// A Google Video hostname can resolve to several CDN edges. Do not pin a
		// dead edge in the ten-minute DNS cache: the direct retry should perform
		// a fresh secure lookup and may receive a reachable address.
		c.forgetDNSAddresses(parsed.Hostname())
		log.Printf("relay DNS cache cleared host=%s", parsed.Hostname())
		_ = listener.Close()
		return "", nil, err
	}
	if preferred > 0 {
		relayClients[0], relayClients[preferred] = relayClients[preferred], relayClients[0]
	}
	var firstResponse sync.Once
	var firstPayload sync.Once
	var firstRequest sync.Once
	handler := http.HandlerFunc(func(w http.ResponseWriter, incoming *http.Request) {
		if subtle.ConstantTimeCompare([]byte(incoming.URL.Query().Get("token")), []byte(token)) != 1 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if incoming.Method != http.MethodGet && incoming.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		target := remote
		needsValidation := false
		switch incoming.URL.Path {
		case "/stream":
		case "/fetch":
			target = incoming.URL.Query().Get("url")
			needsValidation = true
		default:
			http.NotFound(w, incoming)
			return
		}
		targetURL, err := url.Parse(target)
		if err != nil || targetURL.Scheme != "https" || targetURL.Host == "" {
			http.Error(w, "invalid upstream URL", http.StatusBadRequest)
			return
		}
		// /stream always uses the immutable URL validated before the listener was
		// opened. Repeating DNS validation for every FFmpeg probe caused random
		// HTTP 400 responses on the console's slow resolver. Only dynamic DASH
		// child URLs supplied through /fetch need validation here.
		if needsValidation {
			validationContext, cancelValidation := context.WithTimeout(incoming.Context(), 3*time.Second)
			err = c.validateRemoteURL(validationContext, targetURL)
			cancelValidation()
			if err != nil {
				http.Error(w, "upstream URL rejected", http.StatusBadRequest)
				return
			}
		}
		firstRequest.Do(func() {
			log.Printf("relay request host=%s method=%s range=%q", targetURL.Hostname(), incoming.Method, incoming.Header.Get("Range"))
		})
		if incoming.Method == http.MethodGet && strings.HasSuffix(strings.ToLower(targetURL.Hostname()), ".googlevideo.com") {
			handled, chunkErr := serveChunkedMedia(w, incoming, targetURL, relayClients)
			if chunkErr != nil {
				timedOut := errors.Is(chunkErr, context.DeadlineExceeded)
				if networkError, ok := chunkErr.(net.Error); ok {
					timedOut = timedOut || networkError.Timeout()
				}
				log.Printf("relay chunk host=%s failed before_start=%t timeout=%t", targetURL.Hostname(), !handled, timedOut)
			}
			if handled {
				return
			}
		}
		var response *http.Response
		for attempt, relayClient := range relayClients {
			request, requestErr := http.NewRequestWithContext(incoming.Context(), incoming.Method, targetURL.String(), nil)
			if requestErr != nil {
				http.Error(w, "invalid upstream request", http.StatusBadGateway)
				return
			}
			request.Header.Set("User-Agent", userAgent)
			request.Header.Set("Accept", "*/*")
			for _, name := range []string{"Range", "If-Range"} {
				if value := incoming.Header.Get(name); value != "" {
					request.Header.Set(name, value)
				}
			}
			response, err = relayClient.Do(request)
			retryable := err != nil || response.StatusCode >= http.StatusBadRequest
			if !retryable || attempt == len(relayClients)-1 {
				break
			}
			if response != nil {
				_ = response.Body.Close()
				response = nil
			}
			log.Printf("relay retry host=%s route=compatibility-proxy", targetURL.Hostname())
		}
		if err != nil {
			timedOut := false
			if networkError, ok := err.(net.Error); ok {
				timedOut = networkError.Timeout()
			}
			log.Printf("relay upstream host=%s failed timeout=%t", targetURL.Hostname(), timedOut)
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		contentType := response.Header.Get("Content-Type")
		if !allowedRelayContentType(contentType) {
			log.Printf("relay rejected host=%s status=%d reason=content-type type=%q", response.Request.URL.Hostname(), response.StatusCode, contentType)
			http.Error(w, "upstream content type rejected", http.StatusBadGateway)
			return
		}
		downstreamStatus := response.StatusCode
		// Invidious Companion may answer an open-ended Range with status 200
		// while still returning Content-Range. Old FFmpeg rejects that
		// contradictory seek response; expose it as the proper 206 locally.
		if incoming.Header.Get("Range") != "" && response.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Range")), "bytes ") {
			downstreamStatus = http.StatusPartialContent
		}
		firstResponse.Do(func() {
			log.Printf("relay upstream host=%s status=%d downstream=%d type=%q range=%q", response.Request.URL.Hostname(), response.StatusCode, downstreamStatus, contentType, incoming.Header.Get("Range"))
		})
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			log.Printf("relay rejected host=%s status=%d type=%q", response.Request.URL.Hostname(), response.StatusCode, contentType)
		}
		if strings.Contains(strings.ToLower(contentType), "dash+xml") {
			manifest, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
			if err != nil || len(manifest) > maxResponseBytes {
				http.Error(w, "invalid DASH manifest", http.StatusBadGateway)
				return
			}
			manifest = rewriteDASHManifest(manifest, response.Request.URL, "http://"+listener.Addr().String(), token)
			w.Header().Set("Content-Type", "application/dash+xml")
			w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
			w.WriteHeader(response.StatusCode)
			_, _ = w.Write(manifest)
			return
		}
		for _, name := range []string{"Accept-Ranges", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified"} {
			if value := response.Header.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}
		w.WriteHeader(downstreamStatus)
		if incoming.Method == http.MethodHead {
			return
		}
		reader := bufio.NewReader(response.Body)
		firstPayload.Do(func() { log.Printf("relay media started host=%s", response.Request.URL.Hostname()) })
		_, _ = io.Copy(w, reader)
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() { _ = server.Serve(listener) }()
	stop := func() { _ = server.Close() }
	return "http://" + listener.Addr().String() + "/stream?token=" + url.QueryEscape(token), stop, nil
}

func probeRelayTarget(endpoint *url.URL, clients []*http.Client) (int, error) {
	var lastErr error
	for route, client := range clients {
		probeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		request, err := http.NewRequestWithContext(probeContext, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			cancel()
			return -1, err
		}
		request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", relayProbeSize-1))
		request.Header.Set("Accept", "*/*")
		request.Header.Set("User-Agent", userAgent)
		response, err := client.Do(request)
		if err != nil {
			cancel()
			lastErr = err
			log.Printf("relay probe host=%s route=%d failed", endpoint.Hostname(), route)
			continue
		}
		contentType := response.Header.Get("Content-Type")
		probe, readErr := io.ReadAll(io.LimitReader(response.Body, relayProbeSize))
		_ = response.Body.Close()
		cancel()
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && allowedRelayContentType(contentType) && readErr == nil && len(probe) > 0 && int64(len(probe)) <= relayProbeSize {
			log.Printf("relay probe host=%s route=%d status=%d type=%q", endpoint.Hostname(), route, response.StatusCode, contentType)
			return route, nil
		}
		lastErr = fmt.Errorf("HTTP %d type %q", response.StatusCode, contentType)
		log.Printf("relay probe host=%s route=%d rejected status=%d type=%q", endpoint.Hostname(), route, response.StatusCode, contentType)
	}
	if lastErr == nil {
		lastErr = errors.New("no relay route available")
	}
	return -1, fmt.Errorf("media probe failed: %w", lastErr)
}

// The console's network can take more than five seconds to return a full 64 KiB
// block even when the route is usable. Keep the availability probe tiny, then
// use the proven 64 KiB request size and forward bytes immediately.
const (
	relayProbeSize int64 = 2
	relayChunkSize int64 = 64 << 10
)

func serveChunkedMedia(w http.ResponseWriter, incoming *http.Request, endpoint *url.URL, clients []*http.Client) (bool, error) {
	start, requestedEnd, ok := parseByteRange(incoming.Header.Get("Range"))
	if !ok || len(clients) == 0 {
		return false, nil
	}
	cursor := start
	total := int64(-1)
	started := false
	flushed := false
	buffer := make([]byte, 16<<10)
	for {
		chunkEnd := cursor + relayChunkSize - 1
		if requestedEnd >= 0 && chunkEnd > requestedEnd {
			chunkEnd = requestedEnd
		}
		if total >= 0 && chunkEnd >= total {
			chunkEnd = total - 1
		}
		if chunkEnd < cursor {
			return started, nil
		}

		delivered := false
		var lastErr error
		for pass := 0; pass < 2 && !delivered; pass++ {
			for route, client := range clients {
				chunkContext, cancel := context.WithTimeout(incoming.Context(), 15*time.Second)
				request, err := http.NewRequestWithContext(chunkContext, http.MethodGet, endpoint.String(), nil)
				if err != nil {
					cancel()
					return started, err
				}
				request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", cursor, chunkEnd))
				request.Header.Set("Accept", "*/*")
				request.Header.Set("User-Agent", userAgent)
				candidate, err := client.Do(request)
				if err != nil {
					cancel()
					lastErr = err
					continue
				}
				contentType := candidate.Header.Get("Content-Type")
				if candidate.StatusCode < http.StatusOK || candidate.StatusCode >= http.StatusMultipleChoices || !allowedRelayContentType(contentType) {
					lastErr = fmt.Errorf("HTTP %d type %q", candidate.StatusCode, contentType)
					_ = candidate.Body.Close()
					cancel()
					continue
				}
				rangeStart, rangeEnd, rangeTotal, parsed := parseContentRange(candidate.Header.Get("Content-Range"))
				if !parsed || rangeStart != cursor || rangeEnd > chunkEnd || (total >= 0 && rangeTotal != total) {
					lastErr = errors.New("upstream returned the wrong byte range")
					_ = candidate.Body.Close()
					cancel()
					continue
				}
				total = rangeTotal
				if !started {
					if contentType != "" {
						w.Header().Set("Content-Type", contentType)
					}
					w.Header().Set("Accept-Ranges", "bytes")
					finalEnd := requestedEnd
					if finalEnd < 0 || finalEnd >= total {
						finalEnd = total - 1
					}
					w.Header().Set("Content-Length", strconv.FormatInt(finalEnd-start+1, 10))
					if incoming.Header.Get("Range") != "" {
						w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, finalEnd, total))
						w.WriteHeader(http.StatusPartialContent)
					} else {
						w.WriteHeader(http.StatusOK)
					}
					started = true
					log.Printf("relay chunked host=%s start=%d", endpoint.Hostname(), start)
				}

				remaining := rangeEnd - cursor + 1
				for remaining > 0 {
					readSize := int64(len(buffer))
					if remaining < readSize {
						readSize = remaining
					}
					read, readErr := candidate.Body.Read(buffer[:int(readSize)])
					if read > 0 {
						written, writeErr := w.Write(buffer[:read])
						cursor += int64(written)
						remaining -= int64(written)
						if writeErr != nil {
							_ = candidate.Body.Close()
							cancel()
							return true, writeErr
						}
						if written != read {
							_ = candidate.Body.Close()
							cancel()
							return true, io.ErrShortWrite
						}
						if !flushed {
							if flusher, ok := w.(http.Flusher); ok {
								flusher.Flush()
							}
							flushed = true
						}
					}
					if readErr != nil {
						if !errors.Is(readErr, io.EOF) || remaining > 0 {
							lastErr = readErr
						}
						break
					}
					if read == 0 {
						lastErr = io.ErrNoProgress
						break
					}
				}
				_ = candidate.Body.Close()
				cancel()
				if remaining > 0 {
					continue
				}
				delivered = true
				if route > 0 || pass > 0 {
					log.Printf("relay chunk recovered host=%s route=%d pass=%d", endpoint.Hostname(), route, pass)
				}
				break
			}
		}
		if !delivered {
			if lastErr == nil {
				lastErr = errors.New("all chunk routes failed")
			}
			return started, lastErr
		}
		if (requestedEnd >= 0 && cursor > requestedEnd) || (total >= 0 && cursor >= total) {
			return true, nil
		}
	}
}

func parseByteRange(value string) (int64, int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, -1, true
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(parts) != 2 || parts[0] == "" {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end := int64(-1)
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false
		}
	}
	return start, end, true
}

func parseContentRange(value string) (int64, int64, int64, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes "), "/", 2)
	if len(parts) != 2 || parts[1] == "*" {
		return 0, 0, 0, false
	}
	bounds := strings.SplitN(parts[0], "-", 2)
	if len(bounds) != 2 {
		return 0, 0, 0, false
	}
	start, startErr := strconv.ParseInt(bounds[0], 10, 64)
	end, endErr := strconv.ParseInt(bounds[1], 10, 64)
	total, totalErr := strconv.ParseInt(parts[1], 10, 64)
	if startErr != nil || endErr != nil || totalErr != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func (c *Client) httpClientForURL(endpoint *url.URL) *http.Client {
	if c.DirectHTTP == nil || endpoint == nil {
		return c.HTTP
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	if host == pipedMediaProxy || strings.HasSuffix(host, ".googlevideo.com") {
		return c.DirectHTTP
	}
	for _, provider := range c.PipedProviders {
		parsed, err := url.Parse(strings.TrimSpace(provider))
		if err == nil && strings.EqualFold(strings.TrimSuffix(parsed.Hostname(), "."), host) {
			return c.DirectHTTP
		}
	}
	return c.HTTP
}

func allowedRelayContentType(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return strings.HasPrefix(mediaType, "video/") || strings.HasPrefix(mediaType, "audio/") || mediaType == "application/octet-stream" || strings.Contains(mediaType, "dash+xml")
}

func rewriteDASHManifest(manifest []byte, upstreamBase *url.URL, relayBase, token string) []byte {
	return baseURLRE.ReplaceAllFunc(manifest, func(match []byte) []byte {
		parts := baseURLRE.FindSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		reference, err := url.Parse(html.UnescapeString(strings.TrimSpace(string(parts[1]))))
		if err != nil {
			return match
		}
		resolved := upstreamBase.ResolveReference(reference)
		if resolved.Scheme != "https" || resolved.Host == "" {
			return match
		}
		local, _ := url.Parse(relayBase + "/fetch")
		query := local.Query()
		query.Set("token", token)
		query.Set("url", resolved.String())
		local.RawQuery = query.Encode()
		return []byte("<BaseURL>" + local.String() + "</BaseURL>")
	})
}

func BestThumbnail(video Video) (Thumbnail, bool) {
	if len(video.VideoThumbnails) == 0 {
		return Thumbnail{}, false
	}
	best := video.VideoThumbnails[0]
	bestScore := thumbnailScore(best)
	for _, candidate := range video.VideoThumbnails[1:] {
		score := thumbnailScore(candidate)
		if score > bestScore {
			best, bestScore = candidate, score
		}
	}
	return best, best.URL != ""
}

func thumbnailScore(thumbnail Thumbnail) int {
	if thumbnail.URL == "" {
		return -1
	}
	// Prefer a medium thumbnail: enough detail for 146x88 without wasting RAM.
	area := thumbnail.Width * thumbnail.Height
	if area <= 0 {
		area = 1
	}
	if area > 640*480 {
		return 640*480 - (area - 640*480)
	}
	return area
}

func SelectProgressive(formats []Format) (Format, error) {
	type candidate struct {
		format Format
		score  int
	}
	var candidates []candidate
	for _, format := range formats {
		if format.URL == "" {
			continue
		}
		quality := format.QualityLabel
		if quality == "" {
			quality = format.Quality
		}
		height := parseHeight(quality)
		if height == 0 || height > 480 {
			continue
		}
		container := strings.ToLower(format.Container + " " + format.Type)
		score := height
		if strings.Contains(container, "mp4") {
			score += 1000
		}
		if format.Itag == "18" {
			score += 100
		}
		if parsed, err := url.Parse(format.URL); err == nil && parsed.Query().Get("local") == "true" {
			score += 50
		}
		candidates = append(candidates, candidate{format: format, score: score})
	}
	if len(candidates) == 0 {
		return Format{}, errors.New("no progressive stream at 480p or lower")
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	return candidates[0].format, nil
}

func parseHeight(label string) int {
	label = strings.TrimSpace(strings.ToLower(label))
	label = strings.TrimSuffix(label, "p60")
	label = strings.TrimSuffix(label, "p")
	n, _ := strconv.Atoi(label)
	return n
}

func ResolveURL(provider, stream string) (string, error) {
	base, err := url.Parse(provider)
	if err != nil {
		return "", err
	}
	reference, err := url.Parse(stream)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != "https" {
		return "", errors.New("refusing non-HTTPS video URL")
	}
	return resolved.String(), nil
}
