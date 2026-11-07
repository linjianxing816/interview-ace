// Command genicon 生成 InterviewAce 的 exe 图标。
//
// 为什么自己画：找现成图标有版权问题，用图片编辑器又不方便版本化。
// 用代码画的好处是改颜色/形状只改常量，重新生成就行。
//
// 图形：紫→蓝→青对角渐变的圆角方块 + 白色对话气泡（面试就是问答），
// 气泡里三个点表示「正在对话」。小尺寸下也能认出来。
//
// 注意：改完图标要重新生成 ico → 重新生成 syso → 重编 exe，三步缺一不可；
// 而且桌面上还会继续显示旧图标，因为 Windows 的 iconcache 按路径缓存，
// 得手动删 %LOCALAPPDATA%\Microsoft\Windows\Explorer\iconcache*.db（踩坑 #26）。
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// 调色板。改这里就能换配色。
//
// 背景是沿对角线渐变的三个锚点，和前端 favicon.svg 用同一组颜色——
// 浏览器标签页、桌面快捷方式、任务栏三处的图标必须长得一样，
// 不然用户会以为是两个不同的东西。
var (
	bgFrom      = color.NRGBA{R: 0x8B, G: 0x5C, B: 0xF6, A: 0xff} // #8B5CF6 紫
	bgMid       = color.NRGBA{R: 0x3B, G: 0x82, B: 0xF6, A: 0xff} // #3B82F6 蓝
	bgTo        = color.NRGBA{R: 0x22, G: 0xD3, B: 0xEE, A: 0xff} // #22D3EE 青
	bubbleColor = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff} // 白
	dotColor    = color.NRGBA{R: 0x3B, G: 0x82, B: 0xF6, A: 0xff} // 气泡内的点用蓝，比紫更清楚
)

// sizes 是 ICO 里要包含的尺寸。
// Windows 会按显示场景挑：16 任务栏、32 桌面、48 详细信息、256 大图标。
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

// supersample 是超采样倍数：先按 N 倍尺寸画，再盒式降采样得到抗锯齿边缘。
// Go 标准库没有抗锯齿绘图，这是最省事的办法。
const supersample = 4

func main() {
	out := "icon.ico"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	images := make([]image.Image, 0, len(sizes))
	for _, s := range sizes {
		images = append(images, render(s))
	}

	data, err := encodeICO(sizes, images)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成 ICO 失败:", err)
		os.Exit(1)
	}

	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写文件失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s（%d 个尺寸，共 %d 字节）\n", out, len(sizes), len(data))
}

// render 画出指定边长的图标。
func render(size int) image.Image {
	n := size * supersample
	canvas := image.NewNRGBA(image.Rect(0, 0, n, n))

	// 用 0-1 的归一化坐标描述形状，这样任何尺寸比例都一样
	f := float64(n)

	// 1. 圆角方块背景（对角渐变）
	radius := 0.22 * f
	fillRoundedRectGradient(canvas, 0, 0, f, f, radius)

	// 2. 对话气泡（圆角矩形）
	bx, by := 0.17*f, 0.24*f
	bw, bh := 0.66*f, 0.42*f
	fillRoundedRect(canvas, bx, by, bw, bh, 0.11*f, bubbleColor)

	// 3. 气泡尾巴（左下角的三角）
	fillTriangle(canvas,
		0.29*f, by+bh-1, // 左上
		0.29*f, 0.80*f, // 下尖
		0.47*f, by+bh-1, // 右上
		bubbleColor)

	// 4. 三个点
	dotR := 0.045 * f
	dotY := by + bh/2
	for i, dx := range []float64{0.34, 0.50, 0.66} {
		_ = i
		fillCircle(canvas, dx*f, dotY, dotR, dotColor)
	}

	return downsample(canvas, supersample)
}

// fillRoundedRect 填充圆角矩形。逐像素判断是否在形状内。
func fillRoundedRect(img *image.NRGBA, x, y, w, h, r float64, c color.NRGBA) {
	for py := int(y); py < int(y+h); py++ {
		for px := int(x); px < int(x+w); px++ {
			if inRoundedRect(float64(px)+0.5, float64(py)+0.5, x, y, w, h, r) {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

// fillRoundedRectGradient 填充圆角矩形，颜色沿对角线从 bgFrom 经 bgMid 到 bgTo。
//
// 用两段线性插值而不是多段：图标最大也才 256px，16px 时肉眼根本分不出
// 两段和三段的区别，而多一段就多一组常量要维护。
func fillRoundedRectGradient(img *image.NRGBA, x, y, w, h, r float64) {
	for py := int(y); py < int(y+h); py++ {
		for px := int(x); px < int(x+w); px++ {
			if !inRoundedRect(float64(px)+0.5, float64(py)+0.5, x, y, w, h, r) {
				continue
			}
			// 归一化的对角线位置：左上 0，右下 1
			t := ((float64(px)-x)/w + (float64(py)-y)/h) / 2
			img.SetNRGBA(px, py, gradientAt(t))
		}
	}
}

// gradientAt 取渐变上 t 位置的颜色。0.55 是蓝色锚点的位置，
// 和 favicon.svg 里中间那个 stop 的 offset 对齐。
func gradientAt(t float64) color.NRGBA {
	if t < 0.55 {
		return lerpColor(bgFrom, bgMid, t/0.55)
	}
	return lerpColor(bgMid, bgTo, (t-0.55)/0.45)
}

func lerpColor(a, b color.NRGBA, t float64) color.NRGBA {
	t = clamp(t, 0, 1)
	mix := func(x, y uint8) uint8 {
		return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5)
	}
	return color.NRGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 0xff}
}

func inRoundedRect(px, py, x, y, w, h, r float64) bool {
	if px < x || px > x+w || py < y || py > y+h {
		return false
	}
	// 四个角用圆判断，其余区域直接算在内
	cx := clamp(px, x+r, x+w-r)
	cy := clamp(py, y+r, y+h-r)
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= r*r
}

func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	for py := int(cy - r - 1); py <= int(cy+r+1); py++ {
		for px := int(cx - r - 1); px <= int(cx+r+1); px++ {
			dx, dy := float64(px)+0.5-cx, float64(py)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

// fillTriangle 用重心坐标判断点是否在三角形内。
func fillTriangle(img *image.NRGBA, x1, y1, x2, y2, x3, y3 float64, c color.NRGBA) {
	minX := int(min3(x1, x2, x3)) - 1
	maxX := int(max3(x1, x2, x3)) + 1
	minY := int(min3(y1, y2, y3)) - 1
	maxY := int(max3(y1, y2, y3)) + 1

	for py := minY; py <= maxY; py++ {
		for px := minX; px <= maxX; px++ {
			if inTriangle(float64(px)+0.5, float64(py)+0.5, x1, y1, x2, y2, x3, y3) {
				img.SetNRGBA(px, py, c)
			}
		}
	}
}

func inTriangle(px, py, x1, y1, x2, y2, x3, y3 float64) bool {
	d1 := sign(px, py, x1, y1, x2, y2)
	d2 := sign(px, py, x2, y2, x3, y3)
	d3 := sign(px, py, x3, y3, x1, y1)
	hasNeg := d1 < 0 || d2 < 0 || d3 < 0
	hasPos := d1 > 0 || d2 > 0 || d3 > 0
	return !(hasNeg && hasPos)
}

func sign(px, py, ax, ay, bx, by float64) float64 {
	return (px-bx)*(ay-by) - (ax-bx)*(py-by)
}

// downsample 盒式降采样：把 factor×factor 的像素平均成一个。
func downsample(src *image.NRGBA, factor int) *image.NRGBA {
	w := src.Bounds().Dx() / factor
	h := src.Bounds().Dy() / factor
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, b, a, cnt uint32
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					px := src.NRGBAAt(x*factor+dx, y*factor+dy)
					// 按 alpha 加权，避免边缘出现黑边
					r += uint32(px.R) * uint32(px.A)
					g += uint32(px.G) * uint32(px.A)
					b += uint32(px.B) * uint32(px.A)
					a += uint32(px.A)
					cnt++
				}
			}
			if a == 0 {
				dst.SetNRGBA(x, y, color.NRGBA{})
				continue
			}
			dst.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a),
				G: uint8(g / a),
				B: uint8(b / a),
				A: uint8(a / cnt),
			})
		}
	}
	return dst
}

// encodeICO 拼出 ICO 文件。
//
// 格式：6 字节文件头 + N×16 字节目录项 + 各尺寸图像数据。
// 256 用 PNG 压缩（体积小一个数量级），小尺寸用 32 位 BMP
// （兼容性最好，老版本资源管理器也认）。
func encodeICO(sizes []int, images []image.Image) ([]byte, error) {
	var payloads [][]byte
	for i, img := range images {
		var buf bytes.Buffer
		var err error

		if sizes[i] >= 128 {
			err = png.Encode(&buf, img)
		} else {
			err = encodeBMP(&buf, img)
		}
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, buf.Bytes())
	}

	var out bytes.Buffer
	// ICONDIR
	_ = binary.Write(&out, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&out, binary.LittleEndian, uint16(1)) // type = icon
	_ = binary.Write(&out, binary.LittleEndian, uint16(len(sizes)))

	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		w := byte(s)
		if s >= 256 {
			w = 0 // 0 表示 256
		}
		out.WriteByte(w)
		out.WriteByte(w)
		out.WriteByte(0)                                        // 调色板颜色数
		out.WriteByte(0)                                        // reserved
		_ = binary.Write(&out, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&out, binary.LittleEndian, uint16(32)) // bpp
		_ = binary.Write(&out, binary.LittleEndian, uint32(len(payloads[i])))
		_ = binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(payloads[i])
	}

	for _, p := range payloads {
		out.Write(p)
	}
	return out.Bytes(), nil
}

// encodeBMP 把图像写成 ICO 里的 BMP 格式（BITMAPINFOHEADER + BGRA + AND 掩码）。
func encodeBMP(buf *bytes.Buffer, img image.Image) error {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	// BITMAPINFOHEADER，biHeight 要写成两倍（XOR 图 + AND 掩码）
	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[0:], 40)
	binary.LittleEndian.PutUint32(header[4:], uint32(w))
	binary.LittleEndian.PutUint32(header[8:], uint32(h*2))
	binary.LittleEndian.PutUint16(header[12:], 1)  // planes
	binary.LittleEndian.PutUint16(header[14:], 32) // bpp
	buf.Write(header)

	// 像素数据：自下而上，BGRA
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			buf.WriteByte(c.B)
			buf.WriteByte(c.G)
			buf.WriteByte(c.R)
			buf.WriteByte(c.A)
		}
	}

	// AND 掩码：全 0（靠 alpha 通道做透明），每行按 4 字节对齐
	rowBytes := ((w + 31) / 32) * 4
	buf.Write(make([]byte, rowBytes*h))

	return nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min3(a, b, c float64) float64 {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func max3(a, b, c float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}
