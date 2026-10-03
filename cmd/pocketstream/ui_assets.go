package main

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/Acme-Regular.ttf
var acmeFontData []byte

//go:embed assets/AntonSC-Regular.ttf
var antonFontData []byte

//go:embed assets/logo.png
var logoPNG []byte

//go:embed assets/header.png
var headerPNG []byte

//go:embed assets/startup-screen.png
var startupScreenPNG []byte

//go:embed assets/a.png
var buttonAPNG []byte

//go:embed assets/a-pressed.png
var buttonAPressedPNG []byte

//go:embed assets/b.png
var buttonBPNG []byte

//go:embed assets/b-pressed.png
var buttonBPressedPNG []byte

//go:embed assets/x.png
var buttonXPNG []byte

//go:embed assets/x-pressed.png
var buttonXPressedPNG []byte

//go:embed assets/y.png
var buttonYPNG []byte

//go:embed assets/y-pressed.png
var buttonYPressedPNG []byte

//go:embed assets/dpad-up.png
var dpadUpPNG []byte

//go:embed assets/dpad-right.png
var dpadRightPNG []byte

//go:embed assets/dpad-down.png
var dpadDownPNG []byte

//go:embed assets/dpad-left.png
var dpadLeftPNG []byte

//go:embed assets/loading.png
var loadingPNG []byte

//go:embed assets/loading-frames/*.png
var loadingFrameFS embed.FS

//go:embed assets/start.png
var startPNG []byte

//go:embed assets/select.png
var selectPNG []byte

var (
	uiAssetsOnce  sync.Once
	uiAssets      map[string]image.Image
	uiFonts       map[string]*opentype.Font
	loadingFrames []image.Image
	uiFaces       sync.Map
)

func loadUIAssets() {
	uiAssetsOnce.Do(func() {
		uiAssets = make(map[string]image.Image)
		for name, data := range map[string][]byte{
			"logo": logoPNG, "header": headerPNG, "startup-screen": startupScreenPNG,
			"a": buttonAPNG, "a-pressed": buttonAPressedPNG,
			"b": buttonBPNG, "b-pressed": buttonBPressedPNG,
			"x": buttonXPNG, "x-pressed": buttonXPressedPNG,
			"y": buttonYPNG, "y-pressed": buttonYPressedPNG,
			"dpad-up": dpadUpPNG, "dpad-right": dpadRightPNG,
			"dpad-down": dpadDownPNG, "dpad-left": dpadLeftPNG,
			"loading": loadingPNG, "start": startPNG, "select": selectPNG,
		} {
			decoded, err := png.Decode(bytes.NewReader(data))
			if err == nil {
				uiAssets[name] = decoded
			}
		}
		uiFonts = make(map[string]*opentype.Font)
		if parsed, err := opentype.Parse(acmeFontData); err == nil {
			uiFonts["acme"] = parsed
		}
		if parsed, err := opentype.Parse(antonFontData); err == nil {
			uiFonts["anton"] = parsed
		}
		for frame := 0; frame < 12; frame++ {
			data, err := loadingFrameFS.ReadFile(fmt.Sprintf("assets/loading-frames/%02d.png", frame))
			if err != nil {
				continue
			}
			decoded, err := png.Decode(bytes.NewReader(data))
			if err == nil {
				loadingFrames = append(loadingFrames, decoded)
			}
		}
	})
}

type framebufferImage struct{ fb *framebuffer }

func (target *framebufferImage) ColorModel() color.Model { return color.RGBAModel }
func (target *framebufferImage) Bounds() image.Rectangle { return imagepkgRect() }
func imagepkgRect() image.Rectangle                      { return image.Rect(0, 0, screenWidth, screenHeight) }

func (target *framebufferImage) At(x, y int) color.Color {
	if x < 0 || y < 0 || x >= screenWidth || y >= screenHeight {
		return color.RGBA{}
	}
	px, py := x, y
	if target.fb.flip {
		px, py = screenWidth-1-x, screenHeight-1-y
	}
	i := (py*screenWidth + px) * bytesPerPixel
	pixels := target.fb.back
	if len(pixels) == 0 {
		pixels = target.fb.pix
	}
	return color.RGBA{R: pixels[i+2], G: pixels[i+1], B: pixels[i], A: pixels[i+3]}
}

func (target *framebufferImage) Set(x, y int, value color.Color) {
	r, g, b, a := value.RGBA()
	if a == 0 {
		return
	}
	target.fb.pixel(x, y, bgra8888(uint8(r>>8), uint8(g>>8), uint8(b>>8)))
}

func uiFace(name string, size int) font.Face {
	loadUIAssets()
	key := name + ":" + string(rune(size))
	if cached, ok := uiFaces.Load(key); ok {
		return cached.(font.Face)
	}
	parsed := uiFonts[name]
	if parsed == nil {
		return nil
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	uiFaces.Store(key, face)
	return face
}

func (fb *framebuffer) uiText(x, top, size int, value string, ink uint32) {
	fb.uiTextFont("acme", x, top, size, value, ink)
}

func (fb *framebuffer) uiTextFont(fontName string, x, top, size int, value string, ink uint32) {
	value = strings.ToUpper(value)
	for _, character := range value {
		if character > 126 {
			fb.text(x, top, max(1, size/8), value, ink)
			return
		}
	}
	fb.uiTextFontPreserveCase(fontName, x, top, size, value, ink)
}

func (fb *framebuffer) uiTextPreserveCase(x, top, size int, value string, ink uint32) {
	fb.uiTextFontPreserveCase("acme", x, top, size, value, ink)
}

func (fb *framebuffer) uiTextFontPreserveCase(fontName string, x, top, size int, value string, ink uint32) {
	face := uiFace(fontName, size)
	if face == nil {
		fb.text(x, top, max(1, size/8), value, ink)
		return
	}
	red, green, blue := uint8(ink>>16), uint8(ink>>8), uint8(ink)
	drawer := font.Drawer{
		Dst:  &framebufferImage{fb: fb},
		Src:  image.NewUniform(color.RGBA{R: red, G: green, B: blue, A: 255}),
		Face: face,
		Dot:  fixed.P(x, top+face.Metrics().Ascent.Ceil()),
	}
	drawer.DrawString(value)
}

func uiTextWidth(fontName string, size int, value string) int {
	face := uiFace(fontName, size)
	if face == nil {
		return len([]rune(value)) * 6 * max(1, size/8)
	}
	return font.MeasureString(face, strings.ToUpper(value)).Ceil()
}

func (fb *framebuffer) centeredUIText(center, top, size int, value string, ink uint32) {
	fb.uiText(center-uiTextWidth("acme", size, value)/2, top, size, value, ink)
}

func (fb *framebuffer) drawAsset(name string, x, y int) {
	loadUIAssets()
	source := uiAssets[name]
	if source == nil {
		return
	}
	draw.Draw(&framebufferImage{fb: fb}, source.Bounds().Add(image.Pt(x-source.Bounds().Min.X, y-source.Bounds().Min.Y)), source, source.Bounds().Min, draw.Over)
}

func (fb *framebuffer) drawLoadingFrame(frame, x, y int) {
	loadUIAssets()
	if len(loadingFrames) == 0 {
		fb.drawAsset("loading", x, y)
		return
	}
	source := loadingFrames[frame%len(loadingFrames)]
	draw.Draw(&framebufferImage{fb: fb}, source.Bounds().Add(image.Pt(x-source.Bounds().Min.X, y-source.Bounds().Min.Y)), source, source.Bounds().Min, draw.Over)
}

func (fb *framebuffer) roundedRect(x, y, width, height, radius int, ink uint32) {
	if radius <= 0 {
		fb.rect(x, y, width, height, ink)
		return
	}
	for py := 0; py < height; py++ {
		for px := 0; px < width; px++ {
			nearestX := px
			if nearestX < radius {
				nearestX = radius
			} else if nearestX >= width-radius {
				nearestX = width - radius - 1
			}
			nearestY := py
			if nearestY < radius {
				nearestY = radius
			} else if nearestY >= height-radius {
				nearestY = height - radius - 1
			}
			dx, dy := px-nearestX, py-nearestY
			if dx*dx+dy*dy <= radius*radius {
				fb.pixel(x+px, y+py, ink)
			}
		}
	}
}

func (fb *framebuffer) roundedBorder(x, y, width, height, radius, thickness int, ink, fill uint32) {
	fb.roundedRect(x, y, width, height, radius, ink)
	fb.roundedRect(x+thickness, y+thickness, width-2*thickness, height-2*thickness, max(0, radius-thickness), fill)
}
