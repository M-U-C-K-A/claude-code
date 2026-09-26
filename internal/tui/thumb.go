package tui

// Painting previews drawn in the terminal: each cell is an upper half block
// "▀" whose foreground is one pixel and background the pixel below, so a
// cols×rows preview shows cols×(2·rows) roughly square pixels.

import (
	"image"
	"image/color"
	"os"
	"strings"

	_ "image/jpeg"
	_ "image/png"

	"github.com/charmbracelet/lipgloss"
)

// thumbnail renders the picture at path, cols wide, or "" if it can't be read.
func thumbnail(path string, cols int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return ""
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 || cols <= 0 {
		return ""
	}
	pxH := cols * b.Dy() / b.Dx()
	pxH += pxH % 2 // an even number of pixel rows fills whole cells
	if pxH < 2 {
		pxH = 2
	}
	pixel := func(x, y int) lipgloss.Color {
		// Average the source block that lands on this pixel.
		x0, x1 := b.Min.X+x*b.Dx()/cols, b.Min.X+(x+1)*b.Dx()/cols
		y0, y1 := b.Min.Y+y*b.Dy()/pxH, b.Min.Y+(y+1)*b.Dy()/pxH
		x1, y1 = max(x1, x0+1), max(y1, y0+1)
		stepX, stepY := max(1, (x1-x0)/6), max(1, (y1-y0)/6) // sample, don't read every pixel
		var r, g, bl, n uint32
		for yy := y0; yy < y1; yy += stepY {
			for xx := x0; xx < x1; xx += stepX {
				c := color.RGBAModel.Convert(img.At(xx, yy)).(color.RGBA)
				r, g, bl, n = r+uint32(c.R), g+uint32(c.G), bl+uint32(c.B), n+1
			}
		}
		return lipgloss.Color(hex(r/n, g/n, bl/n))
	}
	var out strings.Builder
	for y := 0; y < pxH; y += 2 {
		if y > 0 {
			out.WriteByte('\n')
		}
		for x := 0; x < cols; x++ {
			out.WriteString(lipgloss.NewStyle().Foreground(pixel(x, y)).Background(pixel(x, y+1)).Render("▀"))
		}
	}
	return out.String()
}

func hex(r, g, b uint32) string {
	const digits = "0123456789abcdef"
	return string([]byte{'#',
		digits[r>>4], digits[r&15],
		digits[g>>4], digits[g&15],
		digits[b>>4], digits[b&15],
	})
}
