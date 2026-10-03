package main

import (
	"fmt"
	"strings"
	"time"
)

var (
	designBlack = bgra8888(30, 30, 30)
	designWhite = bgra8888(255, 255, 255)
	designRed   = bgra8888(255, 39, 43)
	designGray  = bgra8888(217, 217, 217)
)

func (a *app) renderHeader(showControls bool) {
	a.fb.rect(0, 0, screenWidth, 60, designBlack)
	a.fb.drawAsset("header", 0, 0)
	if !showControls {
		return
	}
	a.fb.uiText(518, 14, 20, "MENU", designWhite)
	a.fb.uiText(596, 14, 20, keyboardLayouts[a.kbLayout].code, designWhite)
	a.fb.drawAsset("start", 507, 33)
	a.fb.drawAsset("select", 572, 33)
}

func (a *app) pressedAsset(normal string, key uint16) string {
	if a.pressedKey == key {
		return normal + "-pressed"
	}
	return normal
}

func (a *app) dpadAsset() string {
	switch a.pressedKey {
	case keyUp:
		return "dpad-up"
	case keyRight:
		return "dpad-right"
	case keyLeft:
		return "dpad-left"
	case keyDown:
		return "dpad-down"
	default:
		return "dpad-down"
	}
}

func (a *app) renderFooter(labels [4]string) {
	a.fb.rect(0, 430, screenWidth, 50, designBlack)
	positions := [4]int{0, 98, 196, 316}
	if labels[0] == "TYPE" {
		positions[1] = 94
	}
	if labels[0] == "SELECT" {
		positions[1] = 106
	}
	buttons := []struct {
		name string
		key  uint16
		x    int
	}{
		{"a", keySpace, positions[0]}, {"b", keyLeftCtrl, positions[1]},
		{"x", keyLeftShift, positions[2]}, {"y", keyLeftAlt, positions[3]},
	}
	for index, button := range buttons {
		if labels[index] == "" {
			continue
		}
		a.fb.drawAsset(a.pressedAsset(button.name, button.key), button.x, 430)
		a.fb.uiText(button.x+46, 443, 20, labels[index], designWhite)
	}
	a.fb.drawAsset(a.dpadAsset(), 590, 430)
}

func (a *app) renderGrid() {
	a.fb.clear(designWhite)
	a.renderHeader(false)

	for local := 0; local < gridPageSize; local++ {
		row, column := local/gridColumns, local%gridColumns
		x := gridMargin + column*(gridCellWidth+gridGap)
		y := gridTop + row*(gridCellHeight+15)
		a.fb.rect(x, y, gridCellWidth, thumbHeight, designGray)
		a.fb.rect(x, y+thumbHeight, gridCellWidth, gridCellHeight-thumbHeight, designWhite)

		resultIndex := a.scrollRow*gridColumns + local
		if resultIndex < len(a.results) {
			video := a.results[resultIndex]
			if thumbnail := a.thumbnails[video.VideoID]; thumbnail != nil {
				a.drawThumbnail(x, y, thumbnail)
			}
			a.drawCardTitle(x, y+thumbHeight, gridCellWidth, gridCellHeight-thumbHeight, video.Title)
			if resultIndex == a.selected {
				a.fb.border(x-1, y-1, gridCellWidth+2, gridCellHeight+2, 2, designRed)
				a.fb.rect(x, y+thumbHeight, gridCellWidth, 1, designRed)
			} else {
				a.fb.border(x, y, gridCellWidth, gridCellHeight, 1, bgra8888(0, 0, 0))
				a.fb.rect(x, y+thumbHeight, gridCellWidth, 1, bgra8888(0, 0, 0))
			}
		} else {
			a.fb.border(x, y, gridCellWidth, gridCellHeight, 1, bgra8888(0, 0, 0))
			a.fb.rect(x, y+thumbHeight, gridCellWidth, 1, bgra8888(0, 0, 0))
		}
	}

	if len(a.results) == 0 && a.status != "" {
		a.fb.centeredUIText(screenWidth/2, 240, 24, truncateDisplay(a.status, 40), bgra8888(0, 0, 0))
	}
	a.renderFooter([4]string{"OPEN", "BACK", "SEARCH", "HISTORY"})
}

func (a *app) renderKeyboard() {
	a.fb.clear(designWhite)
	a.renderHeader(true)

	// Search field from the Figma frame.
	a.fb.roundedBorder(23, 104, 600, 50, 20, 1, bgra8888(0, 0, 0), designWhite)
	if a.query != "" {
		a.fb.uiText(39, 113, 24, truncateDisplay(a.query, 44), bgra8888(0, 0, 0))
	}

	rows := a.keyboardRows()
	letterRowCount := len(keyboardLayouts[a.kbLayout].rows)
	for row, values := range rows {
		characters := []rune(values)
		keyWidth, keyHeight, gap, top := 50, 50, 6, 235
		if row == letterRowCount {
			keyHeight, top = 30, 201
		} else if row > letterRowCount {
			// The compact utility row is intentionally not part of the supplied
			// visual. Physical B and Y still provide erase and space.
			continue
		} else if letterRowCount > 3 {
			keyHeight = 42
			top += row * 46
		} else {
			top += row * 54
		}
		rowWidth := len(characters)*keyWidth + (len(characters)-1)*gap
		left := (screenWidth - rowWidth) / 2
		for column, character := range characters {
			x := left + column*(keyWidth+gap)
			selected := row == a.kbRow && column == a.kbCol
			if selected {
				a.fb.roundedRect(x-2, top-2, keyWidth+4, keyHeight+4, 11, designRed)
			}
			a.fb.roundedRect(x, top, keyWidth, keyHeight, 9, bgra8888(0, 0, 0))
			fontName, fontSize := "acme", 26
			if keyHeight == 30 {
				fontName, fontSize = "anton", 16
			}
			text := string(character)
			if keyHeight >= 42 {
				// The bundled display font does not cover every Cyrillic and
				// accented character. The bitmap alphabet does, so every language
				// is readable on the device instead of falling back to tofu boxes.
				scale := 4
				if keyHeight < 50 {
					scale = 3
				}
				a.fb.text(x+(keyWidth-5*scale)/2, top+(keyHeight-7*scale)/2, scale, text, designWhite)
			} else {
				textLeft := x + (keyWidth-uiTextWidth(fontName, fontSize, text))/2
				textTop := top + (keyHeight-fontSize)/2 - 1
				a.fb.uiTextFont(fontName, textLeft, textTop, fontSize, text, designWhite)
			}
		}
	}
	a.renderFooter([4]string{"TYPE", "ERASE", "SEARCH", "SPACE"})
}

func (a *app) renderLoading() {
	a.renderLoadingFrame(0)
}

func (a *app) renderLoadingFrame(frame int) {
	a.fb.clear(designWhite)
	a.renderHeader(false)

	const (
		indicatorCount = 8
		squareSize     = 20
		indicatorGap   = 22
	)
	indicatorWidth := indicatorCount*squareSize + (indicatorCount-1)*indicatorGap
	left := (screenWidth - indicatorWidth) / 2
	active := (frame + 1) % indicatorCount
	for index := 0; index < indicatorCount; index++ {
		x := left + index*(squareSize+indicatorGap)
		if index == active {
			a.fb.border(x, 240, squareSize, squareSize, 1, bgra8888(0, 0, 0))
			continue
		}
		a.fb.roundedRect(x, 240, squareSize, squareSize, 2, bgra8888(0, 0, 0))
	}

	a.fb.rect(0, 430, screenWidth, 50, designBlack)
	a.fb.uiTextPreserveCase(20, 437, 30, "Loading...", designWhite)
}

func (a *app) startLoadingAnimation() func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			a.renderLoadingFrame(frame)
			a.fb.present()
			frame = (frame + 1) % 8
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (a *app) renderQualityMenu() {
	a.fb.clear(designWhite)
	a.renderHeader(false)

	const (
		previewX      = 224
		previewY      = 112
		previewWidth  = 350
		previewHeight = 250
		imageHeight   = 180
	)
	a.fb.rect(previewX, previewY, previewWidth, imageHeight, designGray)
	a.fb.rect(previewX, previewY+imageHeight, previewWidth, previewHeight-imageHeight, designWhite)
	if index := a.currentResultIndex(); index >= 0 && index < len(a.results) {
		video := a.results[index]
		if thumbnail := a.thumbnails[video.VideoID]; thumbnail != nil {
			a.drawThumbnailScaled(previewX, previewY, previewWidth, imageHeight, thumbnail)
		}
		a.drawCardTitle(previewX, previewY+imageHeight, previewWidth, previewHeight-imageHeight, video.Title)
	}
	a.fb.border(previewX, previewY, previewWidth, previewHeight, 1, bgra8888(0, 0, 0))
	a.fb.rect(previewX, previewY+imageHeight, previewWidth, 1, bgra8888(0, 0, 0))

	for index, height := range qualityOptions {
		const (
			left         = 18
			buttonTop    = 112
			buttonWidth  = 150
			buttonHeight = 58
			buttonGap    = 10
		)
		top := buttonTop + index*(buttonHeight+buttonGap)
		border := bgra8888(0, 0, 0)
		if index == a.quality {
			border = designRed
		}
		a.fb.roundedBorder(left, top, buttonWidth, buttonHeight, 10, 1, border, designWhite)
		a.fb.centeredUIText(left+buttonWidth/2, top+10, 30, fmt.Sprintf("%dP", height), bgra8888(0, 0, 0))
	}
	a.renderFooter([4]string{"SELECT", "BACK", "", ""})
}

func (a *app) drawCardTitle(x, y, width, height int, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	for size := 17; size >= 8; size-- {
		lines := wrapUIText(value, size, width-10)
		lineHeight := size + 1
		if len(lines)*lineHeight > height-4 {
			continue
		}
		top := y + (height-len(lines)*lineHeight)/2 - 1
		for _, line := range lines {
			a.fb.uiText(x+5, top, size, line, bgra8888(0, 0, 0))
			top += lineHeight
		}
		return
	}
	// Extremely long unbroken titles are rare; keep every visible character
	// and use the smallest readable size rather than an ellipsis.
	lines := wrapUIText(value, 8, width-10)
	for index, line := range lines {
		if index >= 4 {
			break
		}
		a.fb.uiText(x+5, y+2+index*9, 8, line, bgra8888(0, 0, 0))
	}
}

func wrapUIText(value string, size, maxWidth int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	current := ""
	for _, word := range words {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if uiTextWidth("acme", size, candidate) <= maxWidth {
			current = candidate
			continue
		}
		if current != "" {
			lines = append(lines, current)
			current = ""
		}
		for _, character := range []rune(word) {
			candidate = current + string(character)
			if current != "" && uiTextWidth("acme", size, candidate) > maxWidth {
				lines = append(lines, current)
				current = string(character)
			} else {
				current = candidate
			}
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func (a *app) renderHistory() {
	a.fb.clear(designWhite)
	a.renderHeader(false)
	if len(a.history) == 0 {
		a.fb.centeredUIText(screenWidth/2, 225, 26, "NO SEARCH HISTORY", bgra8888(0, 0, 0))
	} else {
		for index, query := range a.history {
			top := 76 + index*42
			if top >= 422 {
				break
			}
			if index == a.historyIndex {
				a.fb.roundedBorder(22, top, 596, 34, 9, 2, designRed, designWhite)
			} else {
				a.fb.roundedBorder(22, top, 596, 34, 9, 1, bgra8888(0, 0, 0), designWhite)
			}
			a.fb.uiText(34, top+5, 20, truncateDisplay(query, 50), bgra8888(0, 0, 0))
		}
	}
	a.renderFooter([4]string{"SEARCH", "BACK", "NEW", "CLEAR"})
}

func playStartupAnimation(fb *framebuffer) {
	for frame := 0; frame < 24; frame++ {
		renderStartupFrame(fb, frame)
		fb.present()
		time.Sleep(24 * time.Millisecond)
	}
}

func renderStartupFrame(fb *framebuffer, frame int) {
	fb.clear(designBlack)
	if frame < 3 {
		return
	}
	fb.drawAsset("startup-screen", 0, 0)
}

func (a *app) shouldRenderLoading() bool {
	status := strings.ToUpper(a.status)
	return strings.Contains(status, "LOADING") || strings.Contains(status, "SEARCHING") || strings.Contains(status, "RESOLVING")
}
