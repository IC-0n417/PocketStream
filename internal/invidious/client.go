package invidious

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
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
	maxResponseBytes = 2 << 20
	userAgent        = "PocketStream/0.1.0 (OnionOS; Miyoo Mini Plus)"
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
	Providers []string
	Current   int
	HTTP      *http.Client

	// Unit tests use loopback TLS servers. Production callers cannot enable
	// this field because it is intentionally package-private.
	allowPrivateHosts bool
}

func New(providers []string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyAddress := strings.TrimSpace(os.Getenv("POCKETSTREAM_SOCKS5")); proxyAddress != "" {
		transport.Proxy = nil
		transport.DialContext = socks5DialContext(proxyAddress)
	}
	client := &Client{
		Providers: append([]string(nil), providers...),
	}
	client.HTTP = &http.Client{
		Transport:     transport,
		Timeout:       6 * time.Second,
		CheckRedirect: client.checkRedirect,
	}
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
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return errors.New("upstream hostname did not resolve")
	}
	for _, address := range addresses {
		if !isPublicNetworkIP(address.IP) {
			return errors.New("refusing hostname with a private network address")
		}
	}
	return nil
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return DASHTracks{}, err
	}
	req.Header.Set("Accept", "application/dash+xml,application/xml;q=0.9")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return DASHTracks{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DASHTracks{}, fmt.Errorf("DASH manifest HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return DASHTracks{}, err
	}
	if len(body) > maxResponseBytes {
		return DASHTracks{}, errors.New("DASH manifest is too large")
	}
	manifest, err := parseDASHManifest(body)
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
	videoURL, err := resolveDASHBaseURL(resp.Request.URL, bestVideo.BaseURL)
	if err != nil {
		return DASHTracks{}, fmt.Errorf("invalid DASH video URL: %w", err)
	}
	audioURL, err := resolveDASHBaseURL(resp.Request.URL, bestAudio.BaseURL)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("HTML response is too large")
	}
	return body, nil
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

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("invalid JSON endpoint")
	}
	if err := c.validateRemoteURL(ctx, parsed); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(body) > maxResponseBytes {
		return errors.New("response is too large")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}

func (c *Client) FetchBytes(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return nil, errors.New("refusing non-HTTPS asset URL")
	}
	if err := c.validateRemoteURL(ctx, u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/jpeg,image/*;q=0.8")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("asset is too large")
	}
	return body, nil
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
	validationContext, cancelValidation := context.WithTimeout(context.Background(), 3*time.Second)
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
	relayClient := &http.Client{
		Transport:     c.HTTP.Transport,
		CheckRedirect: c.checkRedirect,
	}
	var firstResponse sync.Once
	var firstPayload sync.Once
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
		switch incoming.URL.Path {
		case "/stream":
		case "/fetch":
			target = incoming.URL.Query().Get("url")
		default:
			http.NotFound(w, incoming)
			return
		}
		targetURL, err := url.Parse(target)
		if err != nil || targetURL.Scheme != "https" || targetURL.Host == "" {
			http.Error(w, "invalid upstream URL", http.StatusBadRequest)
			return
		}
		validationContext, cancelValidation := context.WithTimeout(incoming.Context(), 3*time.Second)
		err = c.validateRemoteURL(validationContext, targetURL)
		cancelValidation()
		if err != nil {
			http.Error(w, "upstream URL rejected", http.StatusBadRequest)
			return
		}
		request, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, targetURL.String(), nil)
		if err != nil {
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
		response, err := relayClient.Do(request)
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
			log.Printf("relay rejected host=%s reason=content-type", response.Request.URL.Hostname())
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
