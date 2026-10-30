package service

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/big"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

const (
	captchaAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	captchaLength   = 5
)

func captchaCode(length int) string {
	buf := make([]byte, length)
	max := big.NewInt(int64(len(captchaAlphabet)))
	for i := range buf {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic("系统随机源不可用: " + err.Error())
		}
		buf[i] = captchaAlphabet[n.Int64()]
	}
	return string(buf)
}

func captchaInt(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("系统随机源不可用: " + err.Error())
	}
	return int(v.Int64())
}

// captchaImage 生成带噪点的 PNG 验证码，客户端无法通过读取 DOM 文本直接拿到答案。
func captchaImage(answer string) string {
	const width, height = 260, 80
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 245, G: 246, B: 255, A: 255}}, image.Point{}, draw.Src)

	for i := 0; i < 5; i++ {
		drawLine(img, captchaInt(width), captchaInt(height), captchaInt(width), captchaInt(height), color.RGBA{R: 199, G: 201, B: 232, A: 255})
	}
	for i := 0; i < 55; i++ {
		x, y := captchaInt(width), captchaInt(height)
		img.Set(x, y, color.RGBA{R: 213, G: 215, B: 238, A: 255})
	}

	face := basicfont.Face7x13
	for i, r := range answer {
		x := 18 + i*44 + captchaInt(4)
		y := 45 + captchaInt(10)
		d := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(color.RGBA{R: 75, G: 79, B: 115, A: 255}),
			Face: face,
			Dot:  fixed.P(x, y),
		}
		d.DrawString(string(r))
	}

	scaled := image.NewRGBA(image.Rect(0, 0, width*2, height*2))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := img.At(x, y)
			scaled.Set(x*2, y*2, c)
			scaled.Set(x*2+1, y*2, c)
			scaled.Set(x*2, y*2+1, c)
			scaled.Set(x*2+1, y*2+1, c)
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		panic(fmt.Sprintf("生成验证码失败: %v", err))
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	dx := absInt(x1 - x0)
	dy := -absInt(y1 - y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		img.Set(x0, y0, c)
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
