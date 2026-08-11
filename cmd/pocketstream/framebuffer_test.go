package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"testing"

	"pocketstream/internal/invidious"
)

func TestBGRA8888PixelLayout(t *testing.T) {
	fb := &framebuffer{
		pix:  make([]byte, screenWidth*screenHeight*bytesPerPixel),
		back: make([]byte, screenWidth*screenHeight*bytesPerPixel),
	}
	fb.pixel(1, 0, bgra8888(0x11, 0x22, 0x33))
	fb.present()

	got := fb.pix[bytesPerPixel : 2*bytesPerPixel]
	want := []byte{0x33, 0x22, 0x11, 0xff}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pixel bytes = % x, want % x", got, want)
		}
	}
}

func TestRenderGridPreview(t *testing.T) {
	fb := &framebuffer{
		pix:  make([]byte, screenWidth*screenHeight*bytesPerPixel),
		back: make([]byte, screenWidth*screenHeight*bytesPerPixel),
	}
	application := &app{
		fb:         fb,
		client:     invidious.New(defaultProviders),
		query:      "",
		status:     "6 VIDEOS",
		thumbnails: make(map[string]*thumbnailImage),
	}
	titles := []string{
		"Retro Game Reviews", "Miyoo Mini Setup Guide", "Best NES RPGs", "New Handheld Tech",
		"Mini Console Showcase", "NES Gameplay",
	}
	for index, title := range titles {
		id := string(rune('A' + index))
		application.results = append(application.results, invidious.Video{
			VideoID: id, Title: title, Author: "PIXEL PLAY", LengthSeconds: 555 + index*137, ViewCount: int64(2100 + index*170),
		})
		thumbnail := &thumbnailImage{width: thumbWidth, height: thumbHeight, pixels: make([]uint32, thumbWidth*thumbHeight)}
		for y := 0; y < thumbHeight; y++ {
			for x := 0; x < thumbWidth; x++ {
				thumbnail.pixels[y*thumbWidth+x] = bgra8888(
					uint8((x*3+index*38)%256),
					uint8((y*4+index*27)%256),
					uint8(((x+y)*2+index*51)%256),
				)
			}
		}
		application.thumbnails[id] = thumbnail
	}
	application.render()

	if fb.pix[3] != 0xff {
		t.Fatalf("framebuffer alpha byte = %02x", fb.pix[3])
	}
	writePreviewFromEnv(t, fb, "POCKETSTREAM_PREVIEW")
}

func TestRenderKeyboardPreview(t *testing.T) {
	fb := testFramebuffer()
	application := &app{fb: fb, keyboard: true, query: "MIYOO MINI"}
	application.kbRow = len(application.keyboardRows()) - 1
	application.render()
	writePreviewFromEnv(t, fb, "POCKETSTREAM_KEYBOARD_PREVIEW")
}

func TestRenderRussianKeyboardPreview(t *testing.T) {
	fb := testFramebuffer()
	application := &app{fb: fb, keyboard: true, query: "МИЮ МИНИ", kbLayout: 2, kbRow: 0, kbCol: 0}
	application.render()
	writePreviewFromEnv(t, fb, "POCKETSTREAM_RUSSIAN_PREVIEW")
}

func TestRenderStartupPreview(t *testing.T) {
	fb := testFramebuffer()
	renderStartupFrame(fb, 27)
	fb.present()
	writePreviewFromEnv(t, fb, "POCKETSTREAM_STARTUP_PREVIEW")
}

func TestRenderQualityPreview(t *testing.T) {
	fb := testFramebuffer()
	application := &app{
		fb:          fb,
		qualityMenu: true,
		quality:     2,
		results:     []invidious.Video{{VideoID: "abc123DEF45", Title: "Test video"}},
	}
	application.render()
	writePreviewFromEnv(t, fb, "POCKETSTREAM_QUALITY_PREVIEW")
}

func TestRenderHistoryPreview(t *testing.T) {
	fb := testFramebuffer()
	application := &app{
		fb:           fb,
		historyMode:  true,
		historyIndex: 1,
		history:      []string{"miyoo mini", "retro handhelds", "game development", "linux arm games"},
	}
	application.render()
	writePreviewFromEnv(t, fb, "POCKETSTREAM_HISTORY_PREVIEW")
}

func testFramebuffer() *framebuffer {
	return &framebuffer{
		pix:  make([]byte, screenWidth*screenHeight*bytesPerPixel),
		back: make([]byte, screenWidth*screenHeight*bytesPerPixel),
	}
}

func writePreviewFromEnv(t *testing.T, fb *framebuffer, environment string) {
	t.Helper()
	path := os.Getenv(environment)
	if path == "" {
		return
	}
	preview := image.NewRGBA(image.Rect(0, 0, screenWidth, screenHeight))
	for y := 0; y < screenHeight; y++ {
		for x := 0; x < screenWidth; x++ {
			i := (y*screenWidth + x) * bytesPerPixel
			preview.SetRGBA(x, y, color.RGBA{R: fb.pix[i+2], G: fb.pix[i+1], B: fb.pix[i], A: fb.pix[i+3]})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, preview); err != nil {
		t.Fatal(err)
	}
}

func TestBottomNavigationOpensSearch(t *testing.T) {
	application := &app{
		results:     make([]invidious.Video, gridPageSize),
		selected:    gridColumns,
		navSelected: 0,
	}
	application.handle(inputEvent{code: keyDown})
	if !application.navActive {
		t.Fatal("down from the last card row did not activate bottom navigation")
	}
	application.handle(inputEvent{code: keyRight})
	if application.navSelected != 1 {
		t.Fatalf("selected bottom item = %d, want search item 1", application.navSelected)
	}
	application.handle(inputEvent{code: keySpace})
	if !application.keyboard || application.navActive {
		t.Fatal("activating the search tab did not open the keyboard")
	}
}

func TestKeyboardHasSpaceInGridAndPhysicalYShortcut(t *testing.T) {
	application := &app{keyboard: true}
	application.kbRow = len(application.keyboardRows()) - 1
	application.handle(inputEvent{code: keySpace})
	if application.query != " " {
		t.Fatalf("grid space query = %q, want one space", application.query)
	}
	application.query = ""
	application.handle(inputEvent{code: keyLeftAlt})
	if application.query != " " {
		t.Fatalf("Y shortcut query = %q, want one space", application.query)
	}
}

func TestKeyboardSwitchesToRussianAndTypesCyrillic(t *testing.T) {
	application := &app{keyboard: true}
	application.handle(inputEvent{code: keyT})
	application.handle(inputEvent{code: keyT})
	if keyboardLayouts[application.kbLayout].code != "RU" {
		t.Fatalf("layout = %q, want RU", keyboardLayouts[application.kbLayout].code)
	}
	application.handle(inputEvent{code: keySpace})
	if application.query != "Й" {
		t.Fatalf("query = %q, want Cyrillic Й", application.query)
	}
	if got := displayText("привет"); got != "ПРИВЕТ" {
		t.Fatalf("displayText = %q, want ПРИВЕТ", got)
	}
}

func TestEveryKeyboardLayoutFitsAndHasGlyphs(t *testing.T) {
	if len(keyboardLayouts) != 5 {
		t.Fatalf("layout count = %d, want 5", len(keyboardLayouts))
	}
	for _, layout := range keyboardLayouts {
		for _, row := range layout.rows {
			if length := len([]rune(row)); length > 11 {
				t.Fatalf("%s row %q has %d keys, want at most 11", layout.code, row, length)
			}
			for _, character := range displayText(row) {
				if character >= '0' && character <= '9' {
					t.Fatalf("%s letter row %q contains a digit", layout.code, row)
				}
				if _, ok := glyphs[character]; !ok {
					t.Fatalf("%s has no glyph for %q", layout.code, character)
				}
			}
		}
	}
	if keyboardUtilityRows[0] != "1234567890" {
		t.Fatalf("number row = %q", keyboardUtilityRows[0])
	}
}

func TestLayoutSwitchKeepsSelectionOnNumberRow(t *testing.T) {
	application := &app{keyboard: true, kbRow: len(keyboardLayouts[0].rows)}
	application.handle(inputEvent{code: keyT})
	application.handle(inputEvent{code: keyT})
	application.handle(inputEvent{code: keyT})
	if keyboardLayouts[application.kbLayout].code != "ES" {
		t.Fatalf("layout = %s, want ES", keyboardLayouts[application.kbLayout].code)
	}
	if application.kbRow != len(keyboardLayouts[application.kbLayout].rows) {
		t.Fatalf("row = %d, want ES number row %d", application.kbRow, len(keyboardLayouts[application.kbLayout].rows))
	}
}

func TestPlayOpensQualityMenu(t *testing.T) {
	application := &app{results: []invidious.Video{{VideoID: "abc123DEF45"}}}
	application.handle(inputEvent{code: keySpace})
	if !application.qualityMenu {
		t.Fatal("A on a video did not open the quality menu")
	}
	application.handle(inputEvent{code: keyLeftCtrl})
	if application.qualityMenu {
		t.Fatal("B did not close the quality menu")
	}
}

func TestSearchHistoryPersistsNewestUniqueQueries(t *testing.T) {
	path := t.TempDir() + "/search-history.txt"
	application := &app{historyPath: path}
	application.rememberSearch("miyoo mini")
	application.rememberSearch("retro games")
	application.rememberSearch("MIYOO MINI")
	want := []string{"MIYOO MINI", "retro games"}
	got := loadSearchHistory(path)
	if len(got) != len(want) {
		t.Fatalf("history = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("history = %#v, want %#v", got, want)
		}
	}
}

func TestHistoryYButtonClearsStoredQueries(t *testing.T) {
	path := t.TempDir() + "/search-history.txt"
	application := &app{historyMode: true, historyPath: path}
	application.rememberSearch("private query")
	application.handle(inputEvent{code: keyLeftAlt})
	if len(application.history) != 0 {
		t.Fatalf("history was not cleared: %#v", application.history)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("history file still exists: %v", err)
	}
}

func TestThumbnailDecoderRejectsOversizedDimensions(t *testing.T) {
	oversized := image.NewGray(image.Rect(0, 0, 2049, 1))
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, oversized, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeThumbnail(encoded.Bytes()); err == nil {
		t.Fatal("oversized JPEG dimensions were accepted")
	}
}
