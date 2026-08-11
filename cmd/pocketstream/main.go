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
	appVersion    = "0.1.0"
)

var defaultProviders = []string{
	"https://invidious.tiekoetter.com",
	"https://inv.nadeko.net",
	"https://invidious.nerdvpn.de",
	"https://yt.chocolatemoo53.com",
	"https://invidious.f5.si",
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
	if fb.pix != nil {
		_ = syscall.Munmap(fb.pix)
	}
	if fb.file != nil {
		_ = fb.file.Close()
	}
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

type inputEvent struct{ code uint16 }

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
			if typ == 1 && value == 1 {
				ch <- inputEvent{code: code}
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
}

type keyboardLayout struct {
	code string
	name string
	rows []string
}

var keyboardLayouts = []keyboardLayout{
	{code: "EN", name: "ENGLISH", rows: []string{"QWERTYUIOP", "ASDFGHJKL", "ZXCVBNM"}},
	{code: "DE", name: "DEUTSCH", rows: []string{"QWERTZUIOPÜ", "ASDFGHJKLÖÄ", "YXCVBNMß"}},
	{code: "RU", name: "RUSSIAN", rows: []string{"ЙЦУКЕНГШЩЗХ", "ФЫВАПРОЛДЖЭ", "ЯЧСМИТЬБЮЁ"}},
	{code: "ES", name: "ESPANOL", rows: []string{"QWERTYUIOP", "ASDFGHJKLÑ", "ZXCVBNM", "ÁÉÍÓÚÜ"}},
	{code: "FR", name: "FRANCAIS", rows: []string{"AZERTYUIOP", "QSDFGHJKLM", "WXCVBN", "ÉÈÀÇÙ"}},
}

var keyboardUtilityRows = []string{"1234567890", " -_.?"}

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
	a.section = "SEARCH RESULTS"
	a.selected = 0
	a.page = 0
	a.thumbnails = make(map[string]*thumbnailImage)
	a.render()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	results, provider, err := a.client.Search(ctx, a.query)
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
	a.thumbnails = make(map[string]*thumbnailImage)
	if !networkReady() {
		a.status = "WIFI OFFLINE - NO IP ROUTE"
		return
	}
	a.status = "LOADING RECOMMENDATIONS..."
	a.render()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	results, provider, err := a.client.Trending(ctx)
	if err != nil {
		a.status = "HOME FEED UNAVAILABLE"
		logOperationFailure("recommendations", err)
		return
	}
	a.results = results
	a.provider = provider
	a.status = fmt.Sprintf("%d RECOMMENDATIONS", len(results))
	a.loadPageThumbnails()
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
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	streamURL, format, _, err := a.client.Resolve(ctx, video.VideoID)
	if err != nil {
		cancel()
		a.status = "VIDEO UNAVAILABLE"
		logOperationFailure("resolve", err)
		return
	}
	log.Printf("video resolved format=%s", format.Quality)
	if format.Container == "dash" {
		tracks, trackErr := a.client.ResolveDASHTracksAtMost(ctx, streamURL, maxHeight)
		cancel()
		if trackErr != nil {
			a.status = "DASH TRACKS FAILED"
			logOperationFailure("dash tracks", trackErr)
			return
		}
		videoRelay, stopVideo, relayErr := a.client.StartRelay(tracks.VideoURL)
		if relayErr != nil {
			a.status = "VIDEO RELAY FAILED"
			logOperationFailure("video relay", relayErr)
			return
		}
		defer stopVideo()
		audioRelay, stopAudio, relayErr := a.client.StartRelay(tracks.AudioURL)
		if relayErr != nil {
			a.status = "AUDIO RELAY FAILED"
			logOperationFailure("audio relay", relayErr)
			return
		}
		defer stopAudio()
		log.Printf("selected DASH tracks quality=%s", tracks.Quality)
		a.status = "PLAYING " + tracks.Quality + "  MENU/B: EXIT"
		a.render()
		stopped, playErr := launchFFplayDASH(videoRelay, audioRelay)
		if playErr != nil {
			a.status = "FFPLAY FAILED"
			logOperationFailure("playback", playErr)
		} else if stopped {
			a.suppressExit = true
			a.status = "VIDEO CLOSED"
		} else {
			a.status = "PLAYBACK FINISHED"
		}
		return
	}
	cancel()
	relayURL, stopRelay, err := a.client.StartRelay(streamURL)
	if err != nil {
		a.status = "VIDEO RELAY FAILED"
		logOperationFailure("video relay", err)
		return
	}
	defer stopRelay()
	a.status = "PLAYING " + format.Quality + "  MENU/B: EXIT"
	a.render()
	stopped, playErr := launchFFplay(relayURL)
	if playErr != nil {
		a.status = "FFPLAY FAILED"
		logOperationFailure("playback", playErr)
	} else if stopped {
		a.suppressExit = true
		a.status = "VIDEO CLOSED"
	} else {
		a.status = "PLAYBACK FINISHED"
	}
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
	if a.suppressExit {
		a.suppressExit = false
		if event.code == keyEsc || event.code == keyLeftCtrl {
			return true
		}
	}
	if a.qualityMenu {
		switch event.code {
		case keyLeft, keyRight:
			a.quality ^= 1
		case keyUp, keyDown:
			a.quality = (a.quality + 2) % len(qualityOptions)
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
		case keyEnter:
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
		}
	case keyDown:
		if a.selected+gridColumns < a.resultsOnPage() {
			a.selected += gridColumns
		} else {
			a.navActive = true
			a.navSelected = 0
		}
	case keyLeft:
		if a.selected%gridColumns > 0 {
			a.selected--
		}
	case keyRight:
		if a.selected%gridColumns < gridColumns-1 && a.selected+1 < a.resultsOnPage() {
			a.selected++
		}
	case keySpace:
		a.openQualityMenu()
	case keyLeftShift:
		a.keyboard = true
	case keyT:
		if a.page+1 < a.pageCount() {
			a.page++
			a.selected = 0
			a.loadPageThumbnails()
		}
	case keyE:
		if a.page > 0 {
			a.page--
			a.selected = 0
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
	f, err := os.Open(path)
	if err != nil {
		return append([]string(nil), defaultProviders...)
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
		return append([]string(nil), defaultProviders...)
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
	if provider != "" {
		providers = []string{strings.TrimRight(provider, "/")}
	}
	client := invidious.New(providers)
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
		stream, format, used, err := client.Resolve(ctx, results[0].VideoID)
		if err != nil {
			return err
		}
		fmt.Printf("resolved=%s quality=%s url=%s\n", used, format.Quality, stream)
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
		client:      invidious.New(loadProviders(filepath.Join(directory, "providers.txt"))),
		query:       "",
		status:      "LOADING RECOMMENDATIONS...",
		section:     "RECOMMENDED",
		quality:     1,
		thumbnails:  make(map[string]*thumbnailImage),
		historyPath: filepath.Join(directory, "search-history.txt"),
	}
	application.history = loadSearchHistory(application.historyPath)
	application.render()
	application.loadRecommendations()
	application.render()
	for event := range events {
		if !application.handle(event) {
			break
		}
		application.render()
	}
	fb.clear(bgra8888(0, 0, 0))
	fb.present()
}
