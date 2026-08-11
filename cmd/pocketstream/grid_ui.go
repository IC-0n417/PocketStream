package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"strings"
	"sync"
	"time"

	"pocketstream/internal/invidious"
)

const (
	gridColumns    = 3
	gridRows       = 2
	gridPageSize   = gridColumns * gridRows
	gridMargin     = 8
	gridGap        = 8
	gridTop        = 88
	gridCellWidth  = 203
	gridCellHeight = 164
	thumbWidth     = 203
	thumbHeight    = 114
)

type thumbnailImage struct {
	width  int
	height int
	pixels []uint32
}

func (fb *framebuffer) border(x, y, w, h, thickness int, color uint32) {
	fb.rect(x, y, w, thickness, color)
	fb.rect(x, y+h-thickness, w, thickness, color)
	fb.rect(x, y, thickness, h, color)
	fb.rect(x+w-thickness, y, thickness, h, color)
}

func (fb *framebuffer) rightTriangle(x, y, size int, color uint32) {
	for col := 0; col < size; col++ {
		height := size - col
		fb.rect(x+col, y+(size-height)/2, 1, height, color)
	}
}

func (a *app) renderGrid() {
	nearBlack := bgra8888(15, 15, 15)
	white := bgra8888(255, 255, 255)
	accentRed := bgra8888(255, 0, 0)
	textGray := bgra8888(96, 96, 96)
	divider := bgra8888(220, 220, 220)
	chipGray := bgra8888(245, 245, 245)
	searchGlass := bgra8888(248, 248, 248)

	a.fb.clear(white)

	// Compact application bar with a tiny Miyoo-style console mark.
	a.fb.rect(0, 0, screenWidth, 53, white)
	a.fb.rect(0, 51, screenWidth, 2, divider)
	a.drawMiniConsoleIcon(13, 14, nearBlack, accentRed, white)
	a.fb.text(56, 18, 2, "POCKETSTREAM", nearBlack)

	// Search box fills all space between the PocketStream logo and screen edge.
	// It opens with X, so merely launching the app never performs network I/O.
	a.fb.rect(210, 5, 417, 43, searchGlass)
	a.fb.border(210, 5, 417, 43, 1, divider)
	a.fb.rect(568, 6, 58, 41, chipGray)
	a.fb.rect(568, 5, 1, 43, divider)
	a.drawSearchIcon(587, 16, nearBlack)
	searchText := displayText(a.query)
	searchInk := nearBlack
	if searchText == "" {
		searchText = "SEARCH"
		searchInk = bgra8888(155, 155, 155)
	}
	a.fb.text(226, 17, 2, truncateDisplay(searchText, 27), searchInk)

	// Section status replaces the non-functional topic-chip placeholders.
	a.fb.rect(0, 53, screenWidth, 35, white)
	section := a.section
	if section == "" {
		section = "RECOMMENDED"
	}
	a.fb.text(10, 67, 1, section, nearBlack)
	a.fb.text(166, 67, 1, truncateDisplay(a.status, 67), textGray)
	a.fb.text(600, 67, 1, fmt.Sprintf("%d/%d", a.page+1, a.pageCount()), textGray)

	if len(a.results) == 0 {
		a.renderEmptyHome(nearBlack, textGray)
		a.renderBottomBar(accentRed, nearBlack, textGray, divider, white)
		return
	}

	count := a.resultsOnPage()
	for local := 0; local < gridPageSize; local++ {
		row, col := local/gridColumns, local%gridColumns
		x := gridMargin + col*(gridCellWidth+gridGap)
		y := gridTop + row*(gridCellHeight+4)
		a.fb.rect(x, y, gridCellWidth, gridCellHeight, white)

		if local >= count {
			continue
		}
		video := a.results[a.page*gridPageSize+local]
		if thumbnail := a.thumbnails[video.VideoID]; thumbnail != nil {
			a.drawThumbnail(x, y, thumbnail)
		} else {
			a.drawPlaceholder(x, y, thumbWidth, thumbHeight, video.VideoID)
		}

		duration := formatDuration(video.LengthSeconds)
		boxWidth := len(duration)*6 + 6
		a.fb.rect(x+gridCellWidth-boxWidth-4, y+thumbHeight-15, boxWidth, 12, nearBlack)
		a.fb.text(x+gridCellWidth-boxWidth-1, y+thumbHeight-13, 1, duration, white)

		line1, line2 := titleLines(video.Title, 31)
		a.fb.text(x+3, y+119, 1, line1, nearBlack)
		a.fb.text(x+3, y+129, 1, line2, nearBlack)
		meta := truncateDisplay(video.Author, 16) + "  " + formatViews(video.ViewCount)
		a.fb.text(x+3, y+144, 1, truncateDisplay(meta, 32), textGray)

		if local == a.selected {
			a.fb.border(x, y, gridCellWidth, gridCellHeight-1, 3, accentRed)
		}
	}

	a.renderBottomBar(accentRed, nearBlack, textGray, divider, white)
}

func (a *app) renderEmptyHome(black, gray uint32) {
	a.fb.centeredText(screenWidth/2, 210, 2, truncateDisplay(a.status, 45), black)
	a.fb.centeredText(screenWidth/2, 246, 1, "X SEARCH   HOME RELOADS RECOMMENDATIONS", gray)
}

func (a *app) renderBottomBar(red, black, gray, divider, white uint32) {
	a.fb.rect(0, 423, screenWidth, 57, white)
	a.fb.rect(0, 423, screenWidth, 2, divider)

	centers := []int{80, 240, 400, 560}
	colors := []uint32{red, black, black, gray}
	if a.navActive {
		a.fb.rect(centers[a.navSelected]-75, 425, 150, 53, bgra8888(255, 238, 238))
		a.fb.rect(centers[a.navSelected]-75, 423, 150, 3, red)
		for index := range colors {
			colors[index] = black
		}
		colors[a.navSelected] = red
	}
	a.drawMiniConsoleIcon(centers[0]-17, 428, colors[0], red, white)
	a.drawSearchIcon(centers[1]-10, 431, colors[1])
	a.drawHistoryIcon(centers[2]-10, 430, colors[2])
	for i := 0; i < 15; i++ {
		a.fb.rect(centers[3]-7+i, 430+i, 2, 2, colors[3])
		a.fb.rect(centers[3]+7-i, 430+i, 2, 2, colors[3])
	}

	a.fb.centeredText(centers[0], 454, 1, "HOME", colors[0])
	a.fb.centeredText(centers[1], 454, 1, "SEARCH", colors[1])
	a.fb.centeredText(centers[2], 454, 1, "HISTORY", colors[2])
	a.fb.centeredText(centers[3], 454, 1, "EXIT", colors[3])
}

func (fb *framebuffer) centeredText(center, y, scale int, value string, color uint32) {
	text := displayText(value)
	width := len([]rune(text)) * 6 * scale
	fb.text(center-width/2, y, scale, text, color)
}

func (a *app) renderKeyboard() {
	black := bgra8888(15, 15, 15)
	white := bgra8888(255, 255, 255)
	red := bgra8888(255, 0, 0)
	divider := bgra8888(215, 215, 215)
	background := bgra8888(247, 247, 247)
	a.fb.clear(background)
	a.fb.rect(0, 0, screenWidth, 94, white)
	a.fb.rect(0, 92, screenWidth, 2, divider)
	a.drawMiniConsoleIcon(16, 12, black, red, white)
	a.fb.text(59, 17, 2, "SEARCH", black)
	rows := a.keyboardRows()
	layout := keyboardLayouts[a.kbLayout]
	a.fb.rect(529, 9, 95, 28, background)
	a.fb.border(529, 9, 95, 28, 1, divider)
	a.fb.centeredText(576, 20, 1, layout.code+" "+layout.name, black)
	a.fb.rect(16, 44, 608, 40, bgra8888(248, 248, 248))
	a.fb.border(16, 44, 608, 40, 1, divider)
	a.fb.text(28, 56, 2, truncateDisplay(a.query, 47), black)

	letterRowCount := len(layout.rows)
	keyWidth := 44
	keyHeight := 40
	keyGap := 6
	a.fb.text(16, 282, 1, "123", bgra8888(145, 145, 145))
	a.fb.rect(48, 287, 576, 1, divider)
	for row, chars := range rows {
		characters := []rune(chars)
		rowWidth := len(characters)*keyWidth + (len(characters)-1)*keyGap
		startX := (screenWidth - rowWidth) / 2
		keyTop := 100 + row*46
		if row == letterRowCount {
			keyTop = 298
		} else if row == letterRowCount+1 {
			keyTop = 347
		}
		for col, char := range characters {
			x := startX + col*(keyWidth+keyGap)
			selected := row == a.kbRow && col == a.kbCol
			fill, textColor, border := white, black, divider
			if selected {
				fill, textColor, border = red, white, red
			}
			a.fb.rect(x, keyTop, keyWidth, keyHeight, fill)
			a.fb.border(x, keyTop, keyWidth, keyHeight, 2, border)
			if char == ' ' {
				a.fb.centeredText(x+keyWidth/2, keyTop+16, 1, "SP", textColor)
			} else {
				a.fb.centeredText(x+keyWidth/2, keyTop+10, 3, string(char), textColor)
			}
		}
	}
	a.fb.rect(0, 429, screenWidth, 51, white)
	a.fb.rect(0, 429, screenWidth, 2, divider)
	a.fb.text(13, 449, 1, "A TYPE", black)
	a.fb.rect(91, 440, 24, 24, red)
	a.fb.centeredText(103, 449, 1, "Y", white)
	a.fb.text(123, 449, 1, "SPACE", black)
	a.fb.text(201, 449, 1, "B ERASE", black)
	a.fb.text(292, 449, 1, "L1/R1 LANG", black)
	a.fb.text(416, 449, 1, "START SEARCH", black)
	a.fb.text(532, 449, 1, "MENU BACK", black)
}

func (a *app) renderQualityMenu() {
	black := bgra8888(15, 15, 15)
	white := bgra8888(255, 255, 255)
	red := bgra8888(255, 0, 0)
	light := bgra8888(247, 247, 247)
	divider := bgra8888(215, 215, 215)
	a.fb.clear(light)
	a.fb.rect(0, 0, screenWidth, 54, white)
	a.fb.rect(0, 52, screenWidth, 2, divider)
	a.drawMiniConsoleIcon(16, 14, black, red, white)
	a.fb.text(59, 18, 2, "SELECT QUALITY", black)

	cardWidth := 270
	cardHeight := 148
	gap := 18
	startX := (screenWidth - 2*cardWidth - gap) / 2
	startY := 71
	for index, height := range qualityOptions {
		row, column := index/2, index%2
		x := startX + column*(cardWidth+gap)
		y := startY + row*(cardHeight+16)
		fill, ink, border := white, black, divider
		if index == a.quality {
			fill, ink, border = red, white, red
		}
		a.fb.rect(x, y, cardWidth, cardHeight, fill)
		a.fb.border(x, y, cardWidth, cardHeight, 4, border)
		a.fb.centeredText(x+cardWidth/2, y+55, 5, fmt.Sprintf("%dP", height), ink)
	}
}

func (a *app) renderHistory() {
	black := bgra8888(15, 15, 15)
	white := bgra8888(255, 255, 255)
	red := bgra8888(255, 0, 0)
	gray := bgra8888(105, 105, 105)
	divider := bgra8888(215, 215, 215)
	background := bgra8888(247, 247, 247)
	a.fb.clear(background)
	a.fb.rect(0, 0, screenWidth, 54, white)
	a.fb.rect(0, 52, screenWidth, 2, divider)
	a.drawMiniConsoleIcon(16, 14, black, red, white)
	a.fb.text(59, 18, 2, "SEARCH HISTORY", black)

	if len(a.history) == 0 {
		a.fb.centeredText(screenWidth/2, 218, 2, "NO SEARCH HISTORY", gray)
	} else {
		for index, query := range a.history {
			y := 66 + index*34
			fill, ink := white, black
			if index == a.historyIndex {
				fill, ink = bgra8888(255, 235, 235), red
				a.fb.rect(12, y, 4, 28, red)
			}
			a.fb.rect(16, y, 608, 28, fill)
			a.fb.text(29, y+10, 1, truncateDisplay(query, 93), ink)
		}
	}
	a.fb.rect(0, 429, screenWidth, 51, white)
	a.fb.rect(0, 429, screenWidth, 2, divider)
	a.fb.centeredText(screenWidth/2, 449, 1, "A SEARCH   X NEW   Y CLEAR   B BACK", black)
}

func (a *app) drawMiniConsoleIcon(x, y int, body, accent, screen uint32) {
	a.drawMiniConsoleIconScaled(x, y, 1, body, accent, screen)
}

func (a *app) drawMiniConsoleIconScaled(x, y, scale int, body, accent, screen uint32) {
	a.fb.rect(x+4*scale, y, 26*scale, 2*scale, body)
	a.fb.rect(x, y+2*scale, 34*scale, 22*scale, body)
	a.fb.rect(x+5*scale, y+5*scale, 16*scale, 8*scale, screen)
	a.fb.rect(x+8*scale, y+16*scale, 3*scale, 7*scale, screen)
	a.fb.rect(x+6*scale, y+18*scale, 7*scale, 3*scale, screen)
	a.fb.rect(x+25*scale, y+16*scale, 3*scale, 3*scale, accent)
	a.fb.rect(x+29*scale, y+19*scale, 3*scale, 3*scale, accent)
}

func (a *app) drawHistoryIcon(x, y int, color uint32) {
	a.fb.border(x, y, 19, 17, 2, color)
	a.fb.rect(x+8, y+4, 2, 6, color)
	a.fb.rect(x+9, y+9, 5, 2, color)
}

func playStartupAnimation(fb *framebuffer) {
	for frame := 0; frame < 28; frame++ {
		renderStartupFrame(fb, frame)
		fb.present()
		time.Sleep(24 * time.Millisecond)
	}
}

func renderStartupFrame(fb *framebuffer, frame int) {
	animation := &app{fb: fb}
	background := bgra8888(12, 12, 12)
	white := bgra8888(255, 255, 255)
	muted := bgra8888(145, 145, 145)
	red := bgra8888(255, 0, 0)
	fb.clear(background)
	motion := frame
	if motion > 10 {
		motion = 10
	}
	offset := (10 - motion) * 4
	body := muted
	if frame >= 5 {
		body = white
	}
	animation.drawMiniConsoleIconScaled(269, 120+offset, 3, body, red, background)
	if frame >= 4 {
		fb.centeredText(screenWidth/2, 226, 3, "POCKETSTREAM", white)
	}
	lineFrame := frame - 4
	if lineFrame < 0 {
		lineFrame = 0
	}
	if lineFrame > 16 {
		lineFrame = 16
	}
	lineWidth := lineFrame * 17
	fb.rect((screenWidth-lineWidth)/2, 271, lineWidth, 4, red)
	if frame >= 11 {
		fb.centeredText(screenWidth/2, 297, 1, "VIDEO FOR MIYOO MINI+", muted)
	}
}

func (a *app) drawSearchIcon(x, y int, color uint32) {
	a.fb.border(x, y, 13, 13, 2, color)
	for i := 0; i < 8; i++ {
		a.fb.rect(x+11+i, y+11+i, 2, 2, color)
	}
}

func (a *app) drawWifi(x, y int, color uint32) {
	a.fb.rect(x+9, y+15, 3, 3, color)
	a.fb.rect(x+6, y+11, 9, 2, color)
	a.fb.rect(x+3, y+7, 15, 2, color)
	a.fb.rect(x, y+3, 21, 2, color)
}

func (a *app) drawThumbnail(x, y int, thumbnail *thumbnailImage) {
	for py := 0; py < thumbHeight; py++ {
		sy := py * thumbnail.height / thumbHeight
		for px := 0; px < thumbWidth; px++ {
			sx := px * thumbnail.width / thumbWidth
			pixel := thumbnail.pixels[sy*thumbnail.width+sx]
			a.fb.pixel(x+px, y+py, pixel)
		}
	}
}

func (a *app) drawPlaceholder(x, y, w, h int, seed string) {
	a.fb.rect(x, y, w, h, bgra8888(238, 238, 238))
}

func (a *app) currentResultIndex() int {
	index := a.page*gridPageSize + a.selected
	if index < 0 || index >= len(a.results) {
		return -1
	}
	return index
}

func (a *app) resultsOnPage() int {
	remaining := len(a.results) - a.page*gridPageSize
	if remaining < 0 {
		return 0
	}
	if remaining > gridPageSize {
		return gridPageSize
	}
	return remaining
}

func (a *app) pageCount() int {
	pages := (len(a.results) + gridPageSize - 1) / gridPageSize
	if pages < 1 {
		return 1
	}
	return pages
}

func (a *app) loadPageThumbnails() {
	if a.provider == "" || a.resultsOnPage() == 0 {
		return
	}
	if a.thumbnails == nil {
		a.thumbnails = make(map[string]*thumbnailImage)
	}
	a.status = "LOADING PREVIEWS..."
	a.render()

	var wg sync.WaitGroup
	var mu sync.Mutex
	semaphore := make(chan struct{}, 2)
	start := a.page * gridPageSize
	end := start + a.resultsOnPage()
	for index := start; index < end; index++ {
		video := a.results[index]
		if _, exists := a.thumbnails[video.VideoID]; exists {
			continue
		}
		thumbnail, ok := invidious.BestThumbnail(video)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(videoID string, candidate invidious.Thumbnail) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			endpoint, err := invidious.ResolveURL(a.provider, candidate.URL)
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			data, err := a.client.FetchBytes(ctx, endpoint, 900<<10)
			if err != nil {
				return
			}
			decoded, err := decodeThumbnail(data)
			if err != nil {
				return
			}
			image := quantizeThumbnail(decoded, thumbWidth, thumbHeight)
			mu.Lock()
			a.thumbnails[videoID] = image
			mu.Unlock()
		}(video.VideoID, thumbnail)
	}
	wg.Wait()
	if a.section == "RECOMMENDED" {
		a.status = fmt.Sprintf("%d RECOMMENDATIONS", len(a.results))
	} else {
		a.status = fmt.Sprintf("%d RESULTS", len(a.results))
	}
}

const maxThumbnailPixels = 1024 * 1024

func decodeThumbnail(data []byte) (image.Image, error) {
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 2048 || config.Height > 2048 || config.Width > maxThumbnailPixels/config.Height {
		return nil, errors.New("thumbnail dimensions exceed safety limit")
	}
	return jpeg.Decode(bytes.NewReader(data))
}

func quantizeThumbnail(source image.Image, width, height int) *thumbnailImage {
	bounds := source.Bounds()
	result := &thumbnailImage{width: width, height: height, pixels: make([]uint32, width*height)}
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		return result
	}
	for y := 0; y < height; y++ {
		sy := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/width
			r, g, b, _ := source.At(sx, sy).RGBA()
			red := uint8(r>>8) & 0xf8
			green := uint8(g>>8) & 0xfc
			blue := uint8(b>>8) & 0xf8
			result.pixels[y*width+x] = bgra8888(red, green, blue)
		}
	}
	return result
}

func titleLines(title string, width int) (string, string) {
	text := displayText(title)
	words := strings.Fields(text)
	if len(words) == 0 {
		return "UNTITLED VIDEO", ""
	}
	var lines [2]string
	line := 0
	for _, word := range words {
		wordRunes := []rune(word)
		if len(wordRunes) > width {
			word = string(wordRunes[:width])
		}
		candidate := word
		if lines[line] != "" {
			candidate = lines[line] + " " + word
		}
		if len([]rune(candidate)) <= width {
			lines[line] = candidate
			continue
		}
		if line == 0 {
			line = 1
			lines[line] = word
		}
	}
	return lines[0], lines[1]
}

func truncateDisplay(value string, width int) string {
	runes := []rune(displayText(value))
	if len(runes) <= width {
		return string(runes)
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func formatViews(views int64) string {
	switch {
	case views >= 1_000_000:
		return fmt.Sprintf("%.1FM VIEWS", float64(views)/1_000_000)
	case views >= 1_000:
		return fmt.Sprintf("%.1FK VIEWS", float64(views)/1_000)
	case views > 0:
		return fmt.Sprintf("%d VIEWS", views)
	default:
		return "VIEWS N/A"
	}
}
