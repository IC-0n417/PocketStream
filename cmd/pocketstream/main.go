package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"pocketstream/internal/invidious"
)

const (
	screenWidth   = 640
	screenHeight  = 480
	bytesPerPixel = 4
	appVersion    = "1.0.21"
)

var defaultProviders = []string{
	"https://invidious.tiekoetter.com",
	"https://inv.nadeko.net",
	"https://invidious.nerdvpn.de",
	"https://yt.chocolatemoo53.com",
	"https://invidious.f5.si",
}

var defaultPipedProviders = []string{
	"https://api.piped.private.coffee",
	"https://pipedapi.ducks.party",
	"https://pipedapi.wireway.ch",
}

const (
	keyEsc       = 1
	keyTab       = 15
	keyE         = 18
	keyT         = 20
	keyEnter     = 28
	keyLeftCtrl  = 29
	keyLeftShift = 42
	keyLeftAlt   = 56
	keySpace     = 57
	keyRightCtrl = 97
	keyUp        = 103
	keyLeft      = 105
	keyRight     = 106
	keyDown      = 108
	keyBackspace = 14
)

type framebuffer struct {
	file *os.File
	pix  []byte
	back []byte
	flip bool
}

func openFramebuffer() (*framebuffer, error) {
	f, err := os.OpenFile("/dev/fb0", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	length := screenWidth * screenHeight * bytesPerPixel
	pix, err := syscall.Mmap(int(f.Fd()), 0, length, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, err
	}
	return &framebuffer{file: f, pix: pix, back: make([]byte, length), flip: true}, nil
}

func (fb *framebuffer) close() {
	if fb.file != nil && fb.pix != nil {
		_ = syscall.Munmap(fb.pix)
	}
	if fb.file != nil {
		_ = fb.file.Close()
	}
	fb.pix = nil
	fb.file = nil
}

func (fb *framebuffer) reopen() error {
	fresh, err := openFramebuffer()
	if err != nil {
		return err
	}
	fb.file = fresh.file
	fb.pix = fresh.pix
	if len(fb.back) != len(fresh.back) {
		fb.back = fresh.back
	}
	fb.flip = fresh.flip
	return nil
}

func bgra8888(r, g, b uint8) uint32 {
	// Miyoo's 32-bit framebuffer stores bytes as blue, green, red, alpha.
	return uint32(b) | uint32(g)<<8 | uint32(r)<<16 | uint32(0xff)<<24
}

func (fb *framebuffer) pixel(x, y int, color uint32) {
	if x < 0 || y < 0 || x >= screenWidth || y >= screenHeight {
		return
	}
	if fb.flip {
		x = screenWidth - 1 - x
		y = screenHeight - 1 - y
	}
	i := (y*screenWidth + x) * bytesPerPixel
	target := fb.back
	if len(target) == 0 {
		target = fb.pix
	}
	target[i] = byte(color)
	target[i+1] = byte(color >> 8)
	target[i+2] = byte(color >> 16)
	target[i+3] = byte(color >> 24)
}

func (fb *framebuffer) rect(x, y, w, h int, color uint32) {
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			fb.pixel(px, py, color)
		}
	}
}

func (fb *framebuffer) clear(color uint32) { fb.rect(0, 0, screenWidth, screenHeight, color) }

func (fb *framebuffer) present() {
	if len(fb.back) != 0 {
		copy(fb.pix, fb.back)
	}
}

var glyphs = map[rune][7]byte{
	'A': {14, 17, 17, 31, 17, 17, 17}, 'B': {30, 17, 17, 30, 17, 17, 30},
	'C': {14, 17, 16, 16, 16, 17, 14}, 'D': {30, 17, 17, 17, 17, 17, 30},
	'E': {31, 16, 16, 30, 16, 16, 31}, 'F': {31, 16, 16, 30, 16, 16, 16},
	'G': {14, 17, 16, 23, 17, 17, 15}, 'H': {17, 17, 17, 31, 17, 17, 17},
	'I': {14, 4, 4, 4, 4, 4, 14}, 'J': {7, 2, 2, 2, 18, 18, 12},
	'K': {17, 18, 20, 24, 20, 18, 17}, 'L': {16, 16, 16, 16, 16, 16, 31},
	'M': {17, 27, 21, 21, 17, 17, 17}, 'N': {17, 25, 21, 19, 17, 17, 17},
	'O': {14, 17, 17, 17, 17, 17, 14}, 'P': {30, 17, 17, 30, 16, 16, 16},
	'Q': {14, 17, 17, 17, 21, 18, 13}, 'R': {30, 17, 17, 30, 20, 18, 17},
	'S': {15, 16, 16, 14, 1, 1, 30}, 'T': {31, 4, 4, 4, 4, 4, 4},
	'U': {17, 17, 17, 17, 17, 17, 14}, 'V': {17, 17, 17, 17, 17, 10, 4},
	'W': {17, 17, 17, 21, 21, 21, 10}, 'X': {17, 17, 10, 4, 10, 17, 17},
	'Y': {17, 17, 10, 4, 4, 4, 4}, 'Z': {31, 1, 2, 4, 8, 16, 31},
	'0': {14, 17, 19, 21, 25, 17, 14}, '1': {4, 12, 4, 4, 4, 4, 14},
	'2': {14, 17, 1, 2, 4, 8, 31}, '3': {30, 1, 1, 14, 1, 1, 30},
	'4': {2, 6, 10, 18, 31, 2, 2}, '5': {31, 16, 16, 30, 1, 1, 30},
	'6': {14, 16, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8},
	'8': {14, 17, 17, 14, 17, 17, 14}, '9': {14, 17, 17, 15, 1, 1, 14},
	' ': {}, '?': {14, 17, 1, 2, 4, 0, 4}, '-': {0, 0, 0, 31, 0, 0, 0},
	'_': {0, 0, 0, 0, 0, 0, 31}, '.': {0, 0, 0, 0, 0, 12, 12},
	':': {0, 12, 12, 0, 12, 12, 0}, '/': {1, 2, 2, 4, 8, 8, 16},
	'(': {2, 4, 8, 8, 8, 4, 2}, ')': {8, 4, 2, 2, 2, 4, 8},
	'+': {0, 4, 4, 31, 4, 4, 0}, '!': {4, 4, 4, 4, 4, 0, 4},
}

func init() {
	// Cyrillic letters that share their shape with an existing Latin glyph.
	for target, source := range map[rune]rune{
		'А': 'A', 'В': 'B', 'Е': 'E', 'К': 'K', 'М': 'M', 'Н': 'H',
		'О': 'O', 'Р': 'P', 'С': 'C', 'Т': 'T', 'Х': 'X',
	} {
		glyphs[target] = glyphs[source]
	}
	for character, glyph := range map[rune][7]byte{
		'Б': {30, 16, 16, 30, 17, 17, 30}, 'Г': {31, 16, 16, 16, 16, 16, 16},
		'Д': {14, 10, 10, 10, 17, 31, 17}, 'Ё': {10, 0, 31, 16, 30, 16, 31},
		'Ж': {21, 21, 14, 4, 14, 21, 21}, 'З': {14, 17, 1, 6, 1, 17, 14},
		'И': {17, 17, 19, 21, 25, 17, 17}, 'Й': {10, 4, 17, 19, 21, 25, 17},
		'Л': {3, 5, 9, 17, 17, 17, 17}, 'П': {31, 17, 17, 17, 17, 17, 17},
		'У': {17, 17, 10, 4, 8, 16, 14}, 'Ф': {4, 14, 21, 21, 14, 4, 4},
		'Ц': {17, 17, 17, 17, 17, 31, 1}, 'Ч': {17, 17, 17, 15, 1, 1, 1},
		'Ш': {21, 21, 21, 21, 21, 21, 31}, 'Щ': {21, 21, 21, 21, 21, 31, 1},
		'Ъ': {24, 8, 8, 14, 9, 9, 14}, 'Ы': {17, 17, 17, 29, 21, 21, 29},
		'Ь': {16, 16, 16, 30, 17, 17, 30}, 'Э': {14, 17, 1, 7, 1, 17, 14},
		'Ю': {18, 21, 21, 29, 21, 21, 18}, 'Я': {15, 17, 17, 15, 5, 9, 17},
		'Ä': {10, 0, 14, 17, 31, 17, 17}, 'Ö': {10, 0, 14, 17, 17, 17, 14},
		'Ü': {10, 0, 17, 17, 17, 17, 14}, 'ẞ': {30, 17, 17, 30, 17, 17, 30},
		'ß': {30, 17, 17, 30, 17, 17, 30}, 'Ñ': {10, 5, 17, 25, 21, 19, 17},
		'Á': {2, 4, 14, 17, 31, 17, 17}, 'É': {2, 4, 31, 16, 30, 16, 31},
		'Í': {2, 4, 14, 4, 4, 4, 14}, 'Ó': {2, 4, 14, 17, 17, 17, 14},
		'Ú': {2, 4, 17, 17, 17, 17, 14}, 'È': {8, 4, 31, 16, 30, 16, 31},
		'À': {8, 4, 14, 17, 31, 17, 17}, 'Ç': {14, 17, 16, 16, 17, 14, 4},
		'Ù': {8, 4, 17, 17, 17, 17, 14},
	} {
		glyphs[character] = glyph
	}
}

func displayText(value string) string {
	var b strings.Builder
	for _, r := range value {
		r = unicode.ToUpper(r)
		if _, ok := glyphs[r]; ok {
			b.WriteRune(r)
		} else if r >= 32 && r <= 126 {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}

func (fb *framebuffer) text(x, y, scale int, value string, color uint32) {
	for _, r := range displayText(value) {
		glyph, ok := glyphs[r]
		if !ok {
			glyph = glyphs['?']
		}
		for row, bits := range glyph {
			for col := 0; col < 5; col++ {
				if bits&(1<<uint(4-col)) != 0 {
					fb.rect(x+col*scale, y+row*scale, scale, scale, color)
				}
			}
		}
		x += 6 * scale
	}
}

type inputEvent struct {
	code    uint16
	release bool
}

type recommendationResult struct {
	videos   []invidious.Video
	provider string
	err      error
}

type thumbnailResult struct {
	provider string
	images   map[string]*thumbnailImage
}

func readInput(path string) (<-chan inputEvent, *os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan inputEvent, 8)
	go func() {
		defer close(ch)
		buf := make([]byte, 16)
		for {
			if _, err := f.Read(buf); err != nil {
				return
			}
			typ := binary.LittleEndian.Uint16(buf[8:10])
			code := binary.LittleEndian.Uint16(buf[10:12])
			value := int32(binary.LittleEndian.Uint32(buf[12:16]))
			if typ == 1 && (value == 0 || value == 1) {
				ch <- inputEvent{code: code, release: value == 0}
			}
		}
	}()
	return ch, f, nil
}

type app struct {
	fb           *framebuffer
	client       *invidious.Client
	query        string
	results      []invidious.Video
	selected     int
	page         int
	scrollRow    int
	provider     string
	status       string
	keyboard     bool
	kbRow        int
	kbCol        int
	kbLayout     int
	navActive    bool
	navSelected  int
	thumbnails   map[string]*thumbnailImage
	section      string
	qualityMenu  bool
	quality      int
	suppressExit bool
	historyMode  bool
	history      []string
	historyPath  string
	historyIndex int
	pressedKey   uint16
	homeNextpage string
	homeMore     bool
}

type keyboardLayout struct {
	code string
	name string
	rows []string
}

var keyboardLayouts = []keyboardLayout{
	{code: "EN", name: "ENGLISH", rows: []string{"QWERTYUIOP", "ASDFGHJKL", "ZXCVBNM"}},
	{code: "RU", name: "RUSSIAN", rows: []string{"ЙЦУКЕНГШЩЗХ", "ФЫВАПРОЛДЖЭ", "ЯЧСМИТЬБЮЁ"}},
	{code: "FR", name: "FRANCAIS", rows: []string{"AZERTYUIOP", "QSDFGHJKLM", "WXCVBN", "ÉÈÀÇÙ"}},
	{code: "ES", name: "ESPANOL", rows: []string{"QWERTYUIOP", "ASDFGHJKLÑ", "ZXCVBNM", "ÁÉÍÓÚÜ"}},
	{code: "DE", name: "DEUTSCH", rows: []string{"QWERTZUIOPÜ", "ASDFGHJKLÖÄ", "YXCVBNMß"}},
}

var keyboardUtilityRows = []string{"1234567890"}

var qualityOptions = []int{144, 240, 360, 480}

func (a *app) render() {
	defer a.fb.present()
	if a.qualityMenu {
		a.renderQualityMenu()
		return
	}
	if a.keyboard {
		a.renderKeyboard()
		return
	}
	if a.historyMode {
		a.renderHistory()
		return
	}
	if a.shouldRenderLoading() {
		a.renderLoading()
		return
	}
	a.renderGrid()
}

func formatDuration(total int) string {
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, (total/60)%60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}

func (a *app) search() {
	a.query = strings.TrimSpace(a.query)
	if a.query == "" {
		a.status = "PRESS X TO ENTER A SEARCH"
		return
	}
	a.rememberSearch(a.query)
	if !networkReady() {
		a.status = "WIFI OFFLINE - NO IP ROUTE"
		log.Print("search cancelled: wlan0 has no default route")
		return
	}
	a.status = "SEARCHING - PLEASE WAIT"
	log.Printf("search started length=%d", len([]rune(a.query)))
	a.results = nil
	a.homeMore = false
	a.homeNextpage = ""
	a.section = "SEARCH RESULTS"
	a.selected = 0
	a.page = 0
	a.scrollRow = 0
	a.thumbnails = make(map[string]*thumbnailImage)
	a.render()
	stopLoading := a.startLoadingAnimation()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	results, provider, err := a.client.Search(ctx, a.query)
	stopLoading()
	if err != nil {
		a.status = "SEARCH FAILED - TRY R1"
		logOperationFailure("search", err)
		return
	}
	a.results = results
	a.provider = provider
	a.status = fmt.Sprintf("%d RESULTS", len(results))
	a.loadPageThumbnails()
}

func (a *app) loadRecommendations() {
	a.section = "RECOMMENDED"
	a.query = ""
	a.results = nil
	a.selected = 0
	a.page = 0
	a.scrollRow = 0
	a.thumbnails = make(map[string]*thumbnailImage)
	a.homeMore = true
	a.homeNextpage = ""
	if !networkReady() {
		a.status = "WIFI OFFLINE - NO IP ROUTE"
		return
	}
	a.status = "LOADING RECOMMENDATIONS..."
	a.render()
	stopLoading := a.startLoadingAnimation()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	results, provider, err := a.client.Trending(ctx)
	stopLoading()
	if err != nil {
		a.status = "HOME FEED UNAVAILABLE"
		log.Printf("recommendations detail: %v", err)
		logOperationFailure("recommendations", err)
		return
	}
	a.results = results
	a.provider = provider
	a.status = fmt.Sprintf("%d RECOMMENDATIONS", len(results))
	a.loadPageThumbnails()
}

func (a *app) loadMoreRecommendations() bool {
	if a.section != "RECOMMENDED" || !a.homeMore || a.provider == "" {
		return false
	}
	a.status = "LOADING MORE..."
	a.render()
	stopLoading := a.startLoadingAnimation()
	defer stopLoading()

	continuation := a.homeNextpage
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		page, nextpage, err := a.client.MoreRecommendations(ctx, a.provider, continuation)
		cancel()
		if err != nil {
			a.status = "MORE VIDEOS UNAVAILABLE - RETRY"
			logOperationFailure("recommendations continuation", err)
			return false
		}

		a.homeNextpage = nextpage
		a.homeMore = nextpage != ""
		known := make(map[string]bool, len(a.results))
		for _, video := range a.results {
			known[video.VideoID] = true
		}
		added := 0
		for _, video := range page {
			if video.VideoID == "" || known[video.VideoID] {
				continue
			}
			known[video.VideoID] = true
			a.results = append(a.results, video)
			added++
		}
		log.Printf("home continuation added=%d total=%d more=%t", added, len(a.results), a.homeMore)
		if added > 0 {
			a.status = fmt.Sprintf("%d RECOMMENDATIONS", len(a.results))
			return true
		}
		if !a.homeMore {
			a.status = "END OF RECOMMENDATIONS"
			return false
		}
		continuation = nextpage
	}
	a.status = "LOADING MORE - RETRY"
	return false
}

func networkReady() bool {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "00000000" && fields[3] != "0000" {
			return true
		}
	}
	return false
}

func (a *app) play(maxHeight int) {
	index := a.currentResultIndex()
	if index < 0 || index >= len(a.results) {
		return
	}
	video := a.results[index]
	a.status = "RESOLVING VIDEO..."
	a.render()
	stopLoading := a.startLoadingAnimation()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	playback, provider, err := a.client.ResolvePlaybackAtMost(ctx, video.VideoID, maxHeight)
	stopLoading()
	cancel()
	if err != nil {
		a.status = "VIDEO UNAVAILABLE"
		logOperationFailure("resolve", err)
		return
	}
	log.Printf("video resolved quality=%s split=%t", playback.Quality, playback.AudioURL != "")
	stopped, relayFailed, playErr := a.playResolved(playback)
	if relayFailed && provider == "youtube.com" {
		a.status = "RETRYING DIRECT VIDEO..."
		a.render()
		stopRetryLoading := a.startLoadingAnimation()
		retryContext, cancelRetry := context.WithTimeout(context.Background(), 15*time.Second)
		refreshed, retryErr := a.client.ResolveYouTubePlaybackAtMost(retryContext, video.VideoID, maxHeight)
		cancelRetry()
		stopRetryLoading()
		if retryErr == nil {
			log.Printf("direct video URL refreshed quality=%s split=%t", refreshed.Quality, refreshed.AudioURL != "")
			stopped, relayFailed, playErr = a.playResolved(refreshed)
		} else {
			logOperationFailure("direct video refresh", retryErr)
		}
	}
	if relayFailed && provider == "youtube.com" {
		a.status = "TRYING BACKUP STREAM..."
		a.render()
		stopBackupLoading := a.startLoadingAnimation()
		backupContext, cancelBackup := context.WithTimeout(context.Background(), 30*time.Second)
		backup, backupProvider, backupErr := a.client.ResolveFallbackPlaybackAtMost(backupContext, video.VideoID, maxHeight)
		cancelBackup()
		stopBackupLoading()
		if backupErr == nil {
			log.Printf("backup video resolved provider=%s quality=%s split=%t", backupProvider, backup.Quality, backup.AudioURL != "")
			stopped, relayFailed, playErr = a.playResolved(backup)
		} else {
			logOperationFailure("backup resolve", backupErr)
		}
	}
	if playErr != nil {
		if relayFailed {
			a.status = "VIDEO RELAY FAILED"
			logOperationFailure("video relay", playErr)
		} else {
			a.status = "FFPLAY FAILED"
			logOperationFailure("playback", playErr)
		}
	} else if stopped {
		a.suppressExit = true
		a.status = "VIDEO CLOSED"
	} else {
		a.status = "PLAYBACK FINISHED"
	}
}

func (a *app) playResolved(playback invidious.Playback) (bool, bool, error) {
	if playback.AudioURL != "" {
		videoRelay, stopVideo, err := a.client.StartRelay(playback.VideoURL)
		if err != nil {
			return false, true, err
		}
		defer stopVideo()
		audioRelay, stopAudio, err := a.client.StartRelay(playback.AudioURL)
		if err != nil {
			return false, true, err
		}
		defer stopAudio()
		log.Printf("selected split tracks quality=%s", playback.Quality)
		a.status = "PLAYING " + playback.Quality + "  MENU/B: EXIT"
		a.render()
		stopped, err := a.runExternalPlayer(func() (bool, error) {
			return launchFFplayDASH(videoRelay, audioRelay)
		})
		return stopped, false, err
	}

	relayURL, stopRelay, err := a.client.StartRelay(playback.VideoURL)
	if err != nil {
		return false, true, err
	}
	defer stopRelay()
	a.status = "PLAYING " + playback.Quality + "  MENU/B: EXIT"
	a.render()
	stopped, err := a.runExternalPlayer(func() (bool, error) {
		return launchFFplay(relayURL)
	})
	return stopped, false, err
}

func (a *app) runExternalPlayer(play func() (bool, error)) (bool, error) {
	if a.fb == nil || a.fb.file == nil {
		return play()
	}
	a.fb.clear(bgra8888(0, 0, 0))
	a.fb.present()
	a.fb.close()
	stopped, playErr := play()
	if err := a.fb.reopen(); err != nil {
		if playErr != nil {
			return stopped, fmt.Errorf("%v; framebuffer restore failed: %w", playErr, err)
		}
		return stopped, fmt.Errorf("framebuffer restore failed: %w", err)
	}
	return stopped, playErr
}

func launchFFplay(streamURL string) (bool, error) {
	binaryPath := findMediaBinary([]string{
		"/mnt/SDCARD/.tmp_update/bin/ffplay",
		"/mnt/SDCARD/Emu/ffplay/bin/ffplay",
	})
	if binaryPath == "" {
		return false, fmt.Errorf("ffplay not found")
	}
	_ = os.WriteFile("/tmp/stay_awake", nil, 0644)
	defer os.Remove("/tmp/stay_awake")
	cmd := exec.Command(binaryPath, "-autoexit", "-vf", "hflip,vflip", "-i", streamURL)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return false, err
	}
	stopMonitor, stopped := monitorPlaybackExit(cmd.Process)
	err := cmd.Wait()
	stopMonitor()
	if playbackStopped(stopped) {
		return true, nil
	}
	return false, err
}

func launchFFplayDASH(videoURL, audioURL string) (bool, error) {
	ffmpegPath := findMediaBinary([]string{
		"/mnt/SDCARD/App/PocketStream/ffmpeg/ffmpeg",
		"/mnt/SDCARD/.tmp_update/bin/ffmpeg",
		"/mnt/SDCARD/Emu/ffplay/bin/ffmpeg",
	})
	ffplayPath := findMediaBinary([]string{
		"/mnt/SDCARD/.tmp_update/bin/ffplay",
		"/mnt/SDCARD/Emu/ffplay/bin/ffplay",
	})
	if ffmpegPath == "" || ffplayPath == "" {
		return false, fmt.Errorf("ffmpeg/ffplay not found")
	}
	_ = os.WriteFile("/tmp/stay_awake", nil, 0644)
	defer os.Remove("/tmp/stay_awake")

	ffmpeg := exec.Command(ffmpegPath,
		"-loglevel", "warning", "-nostdin", "-max_alloc", "67108864",
		"-protocol_whitelist", "http,tcp", "-i", videoURL,
		"-protocol_whitelist", "http,tcp", "-i", audioURL,
		"-map", "0:v:0", "-map", "1:a:0",
		"-c", "copy", "-shortest", "-f", "matroska", "pipe:1",
	)
	pipe, err := ffmpeg.StdoutPipe()
	if err != nil {
		return false, err
	}
	ffmpeg.Stderr = os.Stderr

	ffplay := exec.Command(ffplayPath, "-autoexit", "-vf", "hflip,vflip", "-i", "pipe:0")
	ffplay.Stdin = pipe
	ffplay.Stdout = os.Stdout
	ffplay.Stderr = os.Stderr
	if err := ffplay.Start(); err != nil {
		return false, err
	}
	if err := ffmpeg.Start(); err != nil {
		_ = ffplay.Process.Kill()
		_ = ffplay.Wait()
		return false, err
	}
	stopMonitor, stopped := monitorPlaybackExit(ffmpeg.Process, ffplay.Process)
	ffmpegErr := ffmpeg.Wait()
	ffplayErr := ffplay.Wait()
	stopMonitor()
	if playbackStopped(stopped) {
		return true, nil
	}
	if ffmpegErr != nil {
		return false, fmt.Errorf("ffmpeg remux failed: %w", ffmpegErr)
	}
	return false, ffplayErr
}

func monitorPlaybackExit(processes ...*os.Process) (func(), <-chan struct{}) {
	events, input, err := readInput("/dev/input/event0")
	if err != nil {
		return func() {}, nil
	}
	stopped := make(chan struct{})
	go func() {
		for event := range events {
			if event.code != keyEsc && event.code != keyLeftCtrl {
				continue
			}
			close(stopped)
			for _, process := range processes {
				if process != nil {
					_ = process.Kill()
				}
			}
			return
		}
	}()
	return func() { _ = input.Close() }, stopped
}

func playbackStopped(stopped <-chan struct{}) bool {
	if stopped == nil {
		return false
	}
	select {
	case <-stopped:
		return true
	default:
		return false
	}
}

func findMediaBinary(paths []string) string {
	for _, candidate := range paths {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func (a *app) handle(event inputEvent) bool {
	if event.release {
		if a.pressedKey == event.code {
			a.pressedKey = 0
			if a.fb != nil {
				a.render()
			}
		}
		return true
	}
	a.pressedKey = event.code
	if a.fb != nil {
		a.render()
	}
	if a.suppressExit {
		a.suppressExit = false
		if event.code == keyEsc || event.code == keyLeftCtrl {
			return true
		}
	}
	if a.qualityMenu {
		switch event.code {
		case keyUp, keyLeft:
			a.quality = (a.quality - 1 + len(qualityOptions)) % len(qualityOptions)
		case keyDown, keyRight:
			a.quality = (a.quality + 1) % len(qualityOptions)
		case keySpace:
			height := qualityOptions[a.quality]
			a.qualityMenu = false
			a.play(height)
		case keyLeftCtrl, keyEsc:
			a.qualityMenu = false
		}
		return true
	}
	if a.historyMode {
		switch event.code {
		case keyUp:
			if len(a.history) > 0 {
				a.historyIndex = (a.historyIndex - 1 + len(a.history)) % len(a.history)
			}
		case keyDown:
			if len(a.history) > 0 {
				a.historyIndex = (a.historyIndex + 1) % len(a.history)
			}
		case keySpace:
			if a.historyIndex >= 0 && a.historyIndex < len(a.history) {
				a.query = a.history[a.historyIndex]
				a.historyMode = false
				a.search()
			}
		case keyLeftShift:
			a.historyMode = false
			a.keyboard = true
		case keyLeftAlt:
			a.clearSearchHistory()
		case keyLeftCtrl, keyEsc:
			a.historyMode = false
		}
		return true
	}
	if a.keyboard {
		rows := a.keyboardRows()
		rowLen := a.keyboardRowLength(a.kbRow)
		switch event.code {
		case keyLeft:
			a.kbCol = (a.kbCol - 1 + rowLen) % rowLen
		case keyRight:
			a.kbCol = (a.kbCol + 1) % rowLen
		case keyUp:
			a.kbRow = (a.kbRow - 1 + len(rows)) % len(rows)
			a.kbCol %= a.keyboardRowLength(a.kbRow)
		case keyDown:
			a.kbRow = (a.kbRow + 1) % len(rows)
			a.kbCol %= a.keyboardRowLength(a.kbRow)
		case keySpace:
			if len([]rune(a.query)) < 45 {
				runes := []rune(rows[a.kbRow])
				a.query += string(runes[a.kbCol])
			}
		case keyLeftAlt:
			if len([]rune(a.query)) < 45 {
				a.query += " "
			}
		case keyLeftCtrl, keyBackspace:
			runes := []rune(a.query)
			if len(runes) > 0 {
				a.query = string(runes[:len(runes)-1])
			}
		case keyEnter, keyLeftShift:
			a.keyboard = false
			a.search()
		case keyE:
			a.switchKeyboardLayout(-1)
		case keyT:
			a.switchKeyboardLayout(1)
		case keyEsc:
			a.keyboard = false
		}
		return true
	}
	if a.navActive {
		switch event.code {
		case keyLeft:
			a.navSelected = (a.navSelected + 3) % 4
		case keyRight:
			a.navSelected = (a.navSelected + 1) % 4
		case keyUp:
			a.navActive = false
		case keySpace:
			return a.activateNavigation()
		case keyLeftShift:
			a.keyboard = true
			a.navActive = false
		case keyLeftCtrl, keyEsc:
			a.navActive = false
		}
		return true
	}
	switch event.code {
	case keyUp:
		if a.selected >= gridColumns {
			a.selected -= gridColumns
			if a.ensureSelectionVisible() {
				a.loadPageThumbnails()
			}
		}
	case keyDown:
		if a.selected+gridColumns < len(a.results) {
			a.selected += gridColumns
			if a.ensureSelectionVisible() {
				a.loadPageThumbnails()
			}
		} else if a.loadMoreRecommendations() && a.selected+gridColumns < len(a.results) {
			a.selected += gridColumns
			if a.ensureSelectionVisible() {
				a.loadPageThumbnails()
			}
		}
	case keyLeft:
		if a.selected%gridColumns > 0 {
			a.selected--
		}
	case keyRight:
		if a.selected%gridColumns < gridColumns-1 && a.selected+1 < len(a.results) {
			a.selected++
		}
	case keySpace:
		a.openQualityMenu()
	case keyLeftShift:
		a.keyboard = true
	case keyLeftAlt:
		a.historyMode = true
		a.historyIndex = 0
	case keyT:
		if a.selected+gridPageSize >= len(a.results) {
			a.loadMoreRecommendations()
		}
		if len(a.results) > 0 && a.selected+gridPageSize < len(a.results) {
			a.selected += gridPageSize
			a.ensureSelectionVisible()
			a.loadPageThumbnails()
		}
	case keyE:
		if a.selected > 0 {
			a.selected -= gridPageSize
			if a.selected < 0 {
				a.selected = 0
			}
			a.ensureSelectionVisible()
			a.loadPageThumbnails()
		}
	case keyLeftCtrl:
		if a.section == "SEARCH RESULTS" {
			a.loadRecommendations()
		} else {
			return false
		}
	case keyEnter:
		a.search()
	case keyEsc:
		return false
	case keyTab, keyRightCtrl:
		// Reserved for later navigation/settings.
	}
	return true
}

func (a *app) keyboardRowLength(row int) int {
	return utf8.RuneCountInString(a.keyboardRows()[row])
}

func (a *app) keyboardRows() []string {
	if a.kbLayout < 0 || a.kbLayout >= len(keyboardLayouts) {
		a.kbLayout = 0
	}
	letters := keyboardLayouts[a.kbLayout].rows
	rows := make([]string, 0, len(letters)+len(keyboardUtilityRows))
	rows = append(rows, letters...)
	rows = append(rows, keyboardUtilityRows...)
	return rows
}

func (a *app) switchKeyboardLayout(delta int) {
	oldLetterRows := len(keyboardLayouts[a.kbLayout].rows)
	utilityRow := a.kbRow - oldLetterRows
	a.kbLayout = (a.kbLayout + delta + len(keyboardLayouts)) % len(keyboardLayouts)
	rows := a.keyboardRows()
	newLetterRows := len(keyboardLayouts[a.kbLayout].rows)
	if utilityRow >= 0 {
		a.kbRow = newLetterRows + utilityRow
	} else if a.kbRow >= newLetterRows {
		a.kbRow = newLetterRows - 1
	}
	if a.kbRow >= len(rows) {
		a.kbRow = len(rows) - 1
	}
	a.kbCol %= a.keyboardRowLength(a.kbRow)
}

func (a *app) openQualityMenu() {
	if a.currentResultIndex() < 0 {
		return
	}
	a.qualityMenu = true
}

func (a *app) activateNavigation() bool {
	switch a.navSelected {
	case 0:
		a.loadRecommendations()
	case 1:
		a.keyboard = true
	case 2:
		a.historyMode = true
		a.historyIndex = 0
	case 3:
		return false
	}
	a.navActive = false
	return true
}

const maxSearchHistory = 10

func loadSearchHistory(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	items := make([]string, 0, maxSearchHistory)
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		query := strings.TrimSpace(scanner.Text())
		key := strings.ToLower(query)
		if query == "" || seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, query)
		if len(items) == maxSearchHistory {
			break
		}
	}
	return items
}

func (a *app) rememberSearch(query string) {
	query = strings.TrimSpace(query)
	if query == "" {
		return
	}
	items := []string{query}
	for _, previous := range a.history {
		if !strings.EqualFold(previous, query) {
			items = append(items, previous)
		}
		if len(items) == maxSearchHistory {
			break
		}
	}
	a.history = items
	if a.historyPath == "" {
		return
	}
	temporary := a.historyPath + ".tmp"
	data := []byte(strings.Join(a.history, "\n") + "\n")
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		logOperationFailure("save search history", err)
		return
	}
	if err := os.Rename(temporary, a.historyPath); err != nil {
		_ = os.Remove(temporary)
		logOperationFailure("save search history", err)
	}
}

func (a *app) clearSearchHistory() {
	a.history = nil
	a.historyIndex = 0
	if a.historyPath == "" {
		return
	}
	_ = os.Remove(a.historyPath + ".tmp")
	if err := os.Remove(a.historyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logOperationFailure("clear search history", err)
	}
}

func logOperationFailure(operation string, err error) {
	category := "unexpected"
	switch {
	case err == nil:
		return
	case errors.Is(err, context.DeadlineExceeded):
		category = "timeout"
	case errors.Is(err, context.Canceled):
		category = "cancelled"
	case strings.Contains(err.Error(), "DASH has no H.264"):
		category = "dash-no-h264"
	case strings.Contains(err.Error(), "DASH has no AAC"):
		category = "dash-no-aac"
	case strings.Contains(err.Error(), "invalid DASH XML"):
		category = "dash-invalid-xml"
	case strings.Contains(err.Error(), "private network") || strings.Contains(err.Error(), "local network host"):
		category = "network-target-rejected"
	case strings.Contains(err.Error(), "hostname did not resolve"):
		category = "dns-resolution"
	case strings.Contains(err.Error(), "HTTP "):
		category = "upstream-http"
	case strings.Contains(err.Error(), "too large"):
		category = "size-limit"
	case strings.Contains(err.Error(), "refusing") || strings.Contains(err.Error(), "invalid"):
		category = "rejected-input"
	case strings.Contains(err.Error(), "not found"):
		category = "missing-component"
	case strings.Contains(err.Error(), "no video") || strings.Contains(err.Error(), "no progressive") || strings.Contains(err.Error(), "no H.264"):
		category = "unsupported-media"
	}
	log.Printf("%s failed category=%s", operation, category)
}

func loadProviders(path string) []string {
	return loadProvidersWithFallback(path, defaultProviders)
}

func loadProvidersWithFallback(path string, fallback []string) []string {
	f, err := os.Open(path)
	if err != nil {
		return append([]string(nil), fallback...)
	}
	defer f.Close()
	var providers []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") && strings.HasPrefix(line, "https://") {
			providers = append(providers, strings.TrimRight(line, "/"))
		}
	}
	if len(providers) == 0 {
		return append([]string(nil), fallback...)
	}
	return providers
}

func appDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func smoke(query, provider string) error {
	providers := defaultProviders
	pipedProviders := defaultPipedProviders
	if provider != "" {
		pipedProviders = []string{strings.TrimRight(provider, "/")}
	}
	client := invidious.NewWithPiped(providers, pipedProviders)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	results, used, err := client.Search(ctx, query)
	if err != nil {
		return err
	}
	fmt.Printf("provider=%s results=%d\n", used, len(results))
	for i, video := range results {
		fmt.Printf("%2d  %s  %s\n", i+1, video.VideoID, video.Title)
	}
	if len(results) > 0 {
		playback, used, err := client.ResolvePlaybackAtMost(ctx, results[0].VideoID, 360)
		if err != nil {
			return err
		}
		fmt.Printf("resolved=%s quality=%s url=%s split=%t\n", used, playback.Quality, playback.VideoURL, playback.AudioURL != "")
	}
	return nil
}

func main() {
	smokeQuery := flag.String("api-smoke", "", "search and resolve without opening the Miyoo UI")
	provider := flag.String("provider", "", "override provider for API smoke test")
	showVersion := flag.Bool("version", false, "print PocketStream version")
	flag.Parse()
	if *showVersion {
		fmt.Println("PocketStream " + appVersion)
		return
	}
	if *smokeQuery != "" {
		if err := smoke(*smokeQuery, *provider); err != nil {
			log.Fatal(err)
		}
		return
	}

	directory := appDir()
	logPath := filepath.Join(directory, "pocketstream.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		defer f.Close()
		log.SetOutput(f)
	}
	fb, err := openFramebuffer()
	if err != nil {
		log.Fatalf("framebuffer: %v", err)
	}
	defer fb.close()
	playStartupAnimation(fb)
	events, input, err := readInput("/dev/input/event0")
	if err != nil {
		log.Fatalf("input: %v", err)
	}
	defer input.Close()

	application := &app{
		fb:          fb,
		client:      invidious.NewWithPiped(loadProviders(filepath.Join(directory, "providers.txt")), loadProvidersWithFallback(filepath.Join(directory, "piped-providers.txt"), defaultPipedProviders)),
		query:       "",
		status:      "LOADING RECOMMENDATIONS...",
		section:     "RECOMMENDED",
		quality:     1,
		thumbnails:  make(map[string]*thumbnailImage),
		historyPath: filepath.Join(directory, "search-history.txt"),
	}
	application.history = loadSearchHistory(application.historyPath)
	log.Printf("PocketStream %s started", appVersion)
	application.render()

	// The first network request must never own the input loop. On slow DNS or
	// an unavailable public provider the old startup path ignored every button
	// until the full timeout elapsed, making the home screen look frozen.
	recommendations := make(chan recommendationResult, 1)
	recommendationContext, cancelRecommendations := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelRecommendations()
	go func() {
		videos, provider, err := application.client.Trending(recommendationContext)
		recommendations <- recommendationResult{videos: videos, provider: provider, err: err}
	}()
	var thumbnails <-chan thumbnailResult
	loadingTicker := time.NewTicker(100 * time.Millisecond)
	defer loadingTicker.Stop()
	loadingFrame := 1
	running := true
	for running {
		select {
		case event, ok := <-events:
			if !ok || !application.handle(event) {
				running = false
				continue
			}
			application.render()
		case result := <-recommendations:
			cancelRecommendations()
			recommendations = nil
			if application.section != "RECOMMENDED" {
				continue
			}
			if result.err != nil {
				application.status = "HOME FEED UNAVAILABLE"
				log.Printf("recommendations detail: %v", result.err)
				logOperationFailure("recommendations", result.err)
				application.render()
				continue
			}
			application.results = result.videos
			application.provider = result.provider
			application.homeMore = true
			application.homeNextpage = ""
			application.status = fmt.Sprintf("%d RECOMMENDATIONS", len(result.videos))
			application.render()
			visible := append([]invidious.Video(nil), result.videos...)
			if len(visible) > gridPageSize {
				visible = visible[:gridPageSize]
			}
			thumbnailChannel := make(chan thumbnailResult, 1)
			thumbnails = thumbnailChannel
			go func(provider string, videos []invidious.Video) {
				thumbnailChannel <- thumbnailResult{provider: provider, images: fetchThumbnails(application.client, provider, videos, nil)}
			}(result.provider, visible)
		case batch := <-thumbnails:
			thumbnails = nil
			if batch.provider != application.provider {
				continue
			}
			for videoID, image := range batch.images {
				application.thumbnails[videoID] = image
			}
			application.render()
		case <-loadingTicker.C:
			if recommendations != nil && !application.keyboard && !application.historyMode && !application.qualityMenu {
				application.renderLoadingFrame(loadingFrame)
				application.fb.present()
				loadingFrame = (loadingFrame + 1) % 8
			}
		}
	}
	fb.clear(bgra8888(0, 0, 0))
	fb.present()
}
