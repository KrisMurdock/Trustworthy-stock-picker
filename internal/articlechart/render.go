package articlechart

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	canvasWidth  = 1100
	canvasHeight = 680
)

var (
	chartColors = struct {
		background    color.RGBA
		panel         color.RGBA
		border        color.RGBA
		ink           color.RGBA
		muted         color.RGBA
		grid          color.RGBA
		up            color.RGBA
		down          color.RGBA
		chanColor     color.RGBA
		zoneColor     color.RGBA
		waveColor     color.RGBA
		fractalBottom color.RGBA
		ma            color.RGBA
	}{
		background:    color.RGBA{R: 255, G: 255, B: 255, A: 255},
		panel:         color.RGBA{R: 248, G: 250, B: 252, A: 255},
		border:        color.RGBA{R: 219, G: 229, B: 240, A: 255},
		ink:           color.RGBA{R: 15, G: 23, B: 42, A: 255},
		muted:         color.RGBA{R: 100, G: 116, B: 139, A: 255},
		grid:          color.RGBA{R: 226, G: 232, B: 240, A: 255},
		up:            color.RGBA{R: 220, G: 38, B: 38, A: 255},
		down:          color.RGBA{R: 22, G: 163, B: 74, A: 255},
		chanColor:     color.RGBA{R: 245, G: 158, B: 11, A: 255},
		zoneColor:     color.RGBA{R: 37, G: 99, B: 235, A: 255},
		waveColor:     color.RGBA{R: 124, G: 58, B: 237, A: 255},
		fractalBottom: color.RGBA{R: 0, G: 122, B: 204, A: 255},
		ma:            color.RGBA{R: 14, G: 165, B: 233, A: 255},
	}

	fontsOnce sync.Once
	regular   *opentype.Font
	mono      *opentype.Font
	fontsErr  error
)

type renderFonts struct {
	title    font.Face
	subtitle font.Face
	label    font.Face
	axis     font.Face
}

func (f renderFonts) close() {
	for _, face := range []font.Face{f.title, f.subtitle, f.label, f.axis} {
		if face != nil {
			_ = face.Close()
		}
	}
}

// RenderPNG renders one validated chart as a deterministic PNG.
func RenderPNG(spec Spec) ([]byte, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	fonts, err := newRenderFonts()
	if err != nil {
		return nil, err
	}
	defer fonts.close()

	img := image.NewRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
	draw.Draw(img, img.Bounds(), image.NewUniform(chartColors.background), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, canvasWidth, 9), image.NewUniform(chartColors.zoneColor), image.Point{}, draw.Src)
	strokeRect(img, image.Rect(12, 12, canvasWidth-12, canvasHeight-12), chartColors.border)
	draw.Draw(img, image.Rect(13, 13, canvasWidth-13, canvasHeight-13), image.NewUniform(chartColors.panel), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(14, 14, canvasWidth-14, canvasHeight-14), image.NewUniform(chartColors.background), image.Point{}, draw.Src)

	drawText(img, fonts.title, 36, 54, chartColors.ink, strings.ToUpper(spec.Code)+"  DAILY STRUCTURE")
	meta := spec.AsOf
	if spec.Adjust != "" {
		meta += " / " + strings.ToUpper(spec.Adjust)
	}
	drawTextRight(img, fonts.subtitle, canvasWidth-38, 51, chartColors.muted, meta)
	chanLine, waveLine := evidenceLines(spec)
	drawText(img, fonts.subtitle, 38, 82, chartColors.muted, chanLine)
	drawText(img, fonts.label, 38, 103, chartColors.muted, waveLine)

	plot := image.Rect(106, 116, 930, 544)
	low, high := chartRange(spec)
	drawPlot(img, plot, spec, low, high, fonts.axis)
	drawLevels(img, plot, spec, low, high, fonts.label)
	drawRetracement(img, spec, fonts)
	drawLegend(img, fonts.axis)
	drawTextRight(img, fonts.axis, canvasWidth-38, canvasHeight-14, chartColors.muted, "RESEARCH OBSERVATION · NOT INVESTMENT ADVICE")

	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&out, img); err != nil {
		return nil, err
	}
	if out.Len() > MaxPNGBytes {
		return nil, fmt.Errorf("chart PNG exceeds %d bytes", MaxPNGBytes)
	}
	return out.Bytes(), nil
}

func newRenderFonts() (renderFonts, error) {
	fontsOnce.Do(func() {
		regular, fontsErr = opentype.Parse(goregular.TTF)
		if fontsErr != nil {
			return
		}
		mono, fontsErr = opentype.Parse(gomono.TTF)
	})
	if fontsErr != nil {
		return renderFonts{}, fontsErr
	}
	title, err := opentype.NewFace(regular, &opentype.FaceOptions{Size: 25, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return renderFonts{}, err
	}
	subtitle, err := opentype.NewFace(mono, &opentype.FaceOptions{Size: 15, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		_ = title.Close()
		return renderFonts{}, err
	}
	label, err := opentype.NewFace(mono, &opentype.FaceOptions{Size: 14, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		_ = title.Close()
		_ = subtitle.Close()
		return renderFonts{}, err
	}
	axis, err := opentype.NewFace(mono, &opentype.FaceOptions{Size: 13, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		_ = title.Close()
		_ = subtitle.Close()
		_ = label.Close()
		return renderFonts{}, err
	}
	return renderFonts{title: title, subtitle: subtitle, label: label, axis: axis}, nil
}

func chartRange(spec Spec) (float64, float64) {
	low, high := spec.Bars[0].Low, spec.Bars[0].High
	for _, bar := range spec.Bars {
		low = math.Min(low, bar.Low)
		high = math.Max(high, bar.High)
	}
	for _, stroke := range spec.Strokes {
		low = math.Min(low, math.Min(stroke.From, stroke.To))
		high = math.Max(high, math.Max(stroke.From, stroke.To))
	}
	for _, point := range spec.Fractals {
		low = math.Min(low, point.Price)
		high = math.Max(high, point.Price)
	}
	if spec.Zone != nil {
		low = math.Min(low, spec.Zone.Lower)
		high = math.Max(high, spec.Zone.Upper)
	}
	for _, level := range spec.Levels {
		low = math.Min(low, level.Price)
		high = math.Max(high, level.Price)
	}
	if high <= low {
		high = low + 1
	}
	padding := (high - low) * 0.08
	if padding <= 0 {
		padding = high * 0.02
	}
	return low - padding, high + padding
}

func drawPlot(img *image.RGBA, plot image.Rectangle, spec Spec, low, high float64, axisFace font.Face) {
	y := func(price float64) int {
		ratio := (price - low) / (high - low)
		return plot.Max.Y - 1 - int(math.Round(ratio*float64(plot.Dy()-1)))
	}
	x := func(index int) int {
		if index < 0 {
			index = 0
		}
		if index >= len(spec.Bars) {
			index = len(spec.Bars) - 1
		}
		return plot.Min.X + int(math.Round((float64(index)+0.5)*float64(plot.Dx())/float64(len(spec.Bars))))
	}
	dateIndex := make(map[string]int, len(spec.Bars))
	for index, bar := range spec.Bars {
		dateIndex[bar.Date] = index
	}
	xForDate := func(date string) int {
		if index, ok := dateIndex[date]; ok {
			return x(index)
		}
		if date == "" || date < spec.Bars[0].Date {
			return plot.Min.X
		}
		return plot.Max.X - 1
	}

	for tick := 0; tick <= 4; tick++ {
		price := low + (high-low)*float64(tick)/4
		py := y(price)
		drawDashedLine(img, plot.Min.X, py, plot.Max.X-1, py, chartColors.grid, 1, 4)
		drawTextRight(img, axisFace, plot.Min.X-10, py+4, chartColors.muted, fmt.Sprintf("%.2f", price))
	}
	for tick := 0; tick <= 4; tick++ {
		index := tick * (len(spec.Bars) - 1) / 4
		px := x(index)
		drawDashedLine(img, px, plot.Min.Y, px, plot.Max.Y-1, chartColors.grid, 1, 4)
		label := spec.Bars[index].Date
		if len(label) >= 10 {
			label = label[5:]
		}
		drawTextCentered(img, axisFace, px, plot.Max.Y+21, chartColors.muted, label)
	}

	if spec.Zone != nil {
		left, right := xForDate(spec.Zone.FromDate), xForDate(spec.Zone.ToDate)
		if right <= left {
			left, right = plot.Min.X, plot.Max.X-1
		}
		top, bottom := y(spec.Zone.Upper), y(spec.Zone.Lower)
		if top > bottom {
			top, bottom = bottom, top
		}
		draw.Draw(img, image.Rect(left, top, right, bottom), image.NewUniform(color.NRGBA{R: 37, G: 99, B: 235, A: 30}), image.Point{}, draw.Over)
		drawDashedLine(img, left, top, right, top, chartColors.zoneColor, 1, 6)
		drawDashedLine(img, left, bottom, right, bottom, chartColors.zoneColor, 1, 6)
		drawText(img, axisFace, left+5, top-5, chartColors.zoneColor, "CHAN ZONE")
	}

	drawMA20(img, plot, spec, low, high)

	slot := float64(plot.Dx()) / float64(len(spec.Bars))
	bodyWidth := int(math.Max(2, math.Round(slot*0.62)))
	for index, bar := range spec.Bars {
		cx := x(index)
		candleColor := chartColors.up
		if bar.Close < bar.Open {
			candleColor = chartColors.down
		}
		drawLine(img, cx, y(bar.High), cx, y(bar.Low), candleColor, 2)
		top, bottom := y(math.Max(bar.Open, bar.Close)), y(math.Min(bar.Open, bar.Close))
		if bottom-top < 2 {
			drawLine(img, cx-bodyWidth/2, top, cx+bodyWidth/2, top, candleColor, 2)
		} else {
			fillRect(img, image.Rect(cx-bodyWidth/2, top, cx+bodyWidth/2+1, bottom+1), candleColor)
		}
	}

	for _, stroke := range spec.Strokes {
		fromX, toX := xForDate(stroke.FromDate), xForDate(stroke.ToDate)
		fromY, toY := y(stroke.From), y(stroke.To)
		if stroke.Finished {
			drawLine(img, fromX, fromY, toX, toY, chartColors.chanColor, 2)
		} else {
			drawDashedLine(img, fromX, fromY, toX, toY, chartColors.chanColor, 2, 5)
		}
		drawCircle(img, fromX, fromY, 3, chartColors.chanColor)
		drawCircle(img, toX, toY, 3, chartColors.chanColor)
	}
	for _, point := range spec.Fractals {
		px, py := xForDate(point.Date), y(point.Price)
		mark := chartColors.ink
		if strings.EqualFold(point.Mark, "bottom") {
			mark = chartColors.fractalBottom
		}
		drawCircle(img, px, py, 3, mark)
	}

	last := spec.Bars[len(spec.Bars)-1]
	lastY := y(last.Close)
	drawDashedLine(img, plot.Min.X, lastY, plot.Max.X-1, lastY, chartColors.ink, 1, 3)
}

func drawMA20(img *image.RGBA, plot image.Rectangle, spec Spec, low, high float64) {
	y := func(price float64) int {
		ratio := (price - low) / (high - low)
		return plot.Max.Y - 1 - int(math.Round(ratio*float64(plot.Dy()-1)))
	}
	x := func(index int) int {
		return plot.Min.X + int(math.Round((float64(index)+0.5)*float64(plot.Dx())/float64(len(spec.Bars))))
	}
	const window = 20
	if len(spec.Bars) < window+1 {
		return
	}
	sum := 0.0
	for index := 0; index < window; index++ {
		sum += spec.Bars[index].Close
	}
	for index := window; index < len(spec.Bars); index++ {
		sum += spec.Bars[index].Close - spec.Bars[index-window].Close
		drawLine(img, x(index-1), y((sum-spec.Bars[index].Close+spec.Bars[index-window].Close)/window), x(index), y(sum/window), chartColors.ma, 2)
	}
}

type levelLabel struct {
	actualY int
	labelY  int
	text    string
	color   color.RGBA
}

func drawLevels(img *image.RGBA, plot image.Rectangle, spec Spec, low, high float64, face font.Face) {
	y := func(price float64) int {
		ratio := (price - low) / (high - low)
		return plot.Max.Y - 1 - int(math.Round(ratio*float64(plot.Dy()-1)))
	}
	labels := make([]levelLabel, 0, len(spec.Levels)+1)
	for _, level := range spec.Levels {
		py := y(level.Price)
		levelColor := levelColor(level.Kind)
		drawDashedLine(img, plot.Min.X, py, plot.Max.X-1, py, levelColor, 1, 7)
		text := strings.ToUpper(level.Label) + " " + fmt.Sprintf("%.2f", level.Price)
		labels = append(labels, levelLabel{actualY: py, labelY: py, text: text, color: levelColor})
	}
	last := spec.Bars[len(spec.Bars)-1]
	labels = append(labels, levelLabel{actualY: y(last.Close), labelY: y(last.Close), text: "LAST " + fmt.Sprintf("%.2f", last.Close), color: chartColors.ink})
	sort.SliceStable(labels, func(i, j int) bool { return labels[i].labelY < labels[j].labelY })
	const gap = 19
	for index := 1; index < len(labels); index++ {
		if labels[index].labelY-labels[index-1].labelY < gap {
			labels[index].labelY = labels[index-1].labelY + gap
		}
	}
	maxY := plot.Max.Y - 6
	for index := len(labels) - 1; index >= 0; index-- {
		if labels[index].labelY > maxY {
			labels[index].labelY = maxY
		}
		if index > 0 && labels[index].labelY-labels[index-1].labelY < gap {
			labels[index-1].labelY = labels[index].labelY - gap
		}
		maxY -= gap
	}
	for _, label := range labels {
		drawLine(img, plot.Max.X, label.actualY, plot.Max.X+12, label.labelY, label.color, 1)
		drawText(img, face, plot.Max.X+17, label.labelY+5, label.color, label.text)
	}
}

func drawRetracement(img *image.RGBA, spec Spec, fonts renderFonts) {
	if spec.RetracementPct <= 0 {
		return
	}
	y := 590
	left, right := 366, 894
	drawText(img, fonts.label, 38, y+5, chartColors.muted, "WAVE RETRACEMENT")
	label := fmt.Sprintf("%.1f%%", spec.RetracementPct)
	if retracementBreakdown(spec) {
		label = ">100% / BREAKDOWN"
	}
	drawText(img, fonts.label, 38, y+27, chartColors.waveColor, label)
	drawLine(img, left, y, right, y, chartColors.grid, 6)
	for _, tick := range []float64{0, 38.2, 61.8, 100} {
		px := left + int(math.Round(float64(right-left)*tick/100))
		drawLine(img, px, y-8, px, y+8, chartColors.muted, 1)
		drawTextCentered(img, fonts.axis, px, y+24, chartColors.muted, fmt.Sprintf("%.1f", tick))
	}
	position := math.Min(100, math.Max(0, spec.RetracementPct))
	px := left + int(math.Round(float64(right-left)*position/100))
	drawCircle(img, px, y, 6, chartColors.waveColor)
	drawCircle(img, px, y, 3, chartColors.background)
}

func drawLegend(img *image.RGBA, face font.Face) {
	y := 636
	x := 38
	item := func(color color.RGBA, label string) {
		drawLine(img, x, y, x+20, y, color, 2)
		drawText(img, face, x+27, y+5, chartColors.muted, label)
		x += 27 + int(math.Round(textWidth(face, label))) + 28
	}
	itemPair := func(topColor, bottomColor color.RGBA, label string) {
		drawCircle(img, x+7, y, 3, topColor)
		drawCircle(img, x+18, y, 3, bottomColor)
		drawText(img, face, x+27, y+5, chartColors.muted, label)
		x += 27 + int(math.Round(textWidth(face, label))) + 28
	}
	item(chartColors.up, "UP")
	item(chartColors.down, "DOWN")
	item(chartColors.chanColor, "CHAN STROKE")
	item(chartColors.ma, "MA20")
	item(chartColors.zoneColor, "CHAN ZONE")
	itemPair(chartColors.ink, chartColors.fractalBottom, "FRACTAL T/B")
	item(chartColors.waveColor, "WAVE LEVEL")
}

func evidenceLines(spec Spec) (string, string) {
	chanParts := []string{
		fmt.Sprintf("%d FRACTALS", len(spec.Fractals)),
		fmt.Sprintf("%d DISPLAY STROKES", len(spec.Strokes)),
	}
	if spec.CompletedStrokes > 0 {
		chanParts = append(chanParts, fmt.Sprintf("%d COMPLETED", spec.CompletedStrokes))
	}
	if spec.Zone != nil {
		chanParts = append(chanParts, fmt.Sprintf("ZONE %.2f-%.2f", spec.Zone.Lower, spec.Zone.Upper))
	} else {
		chanParts = append(chanParts, "ZONE N/A")
	}

	stage := "N/A"
	if spec.Stage != "" {
		stage = strings.ToUpper(spec.Stage)
	}
	waveParts := []string{"STAGE " + stage}
	switch {
	case retracementBreakdown(spec):
		waveParts = append(waveParts, "RETRACE >100% BREAKDOWN")
	case spec.RetracementPct > 0:
		waveParts = append(waveParts, fmt.Sprintf("RETRACE %.1f%%", spec.RetracementPct))
	}
	for _, level := range spec.Levels {
		waveParts = append(waveParts, fmt.Sprintf("%s %.2f", strings.ToUpper(level.Label), level.Price))
	}
	if len(spec.Levels) == 0 {
		waveParts = append(waveParts, "LEVELS N/A")
	}
	return "CHAN: " + strings.Join(chanParts, " · "), "WAVE: " + strings.Join(waveParts, " · ")
}

func retracementBreakdown(spec Spec) bool {
	return spec.RetracementExceeded || spec.RetracementPct > 100
}

func levelColor(kind string) color.RGBA {
	switch strings.ToLower(kind) {
	case "support":
		return chartColors.down
	case "resistance", "target":
		return chartColors.up
	case "invalidation", "stop":
		return chartColors.waveColor
	case "entry":
		return chartColors.zoneColor
	default:
		return chartColors.muted
	}
}

func drawText(img *image.RGBA, face font.Face, x, y int, c color.Color, text string) {
	drawer := font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	drawer.DrawString(text)
}

func drawTextRight(img *image.RGBA, face font.Face, right, y int, c color.Color, text string) {
	drawText(img, face, right-int(math.Round(textWidth(face, text))), y, c, text)
}

func drawTextCentered(img *image.RGBA, face font.Face, center, y int, c color.Color, text string) {
	drawText(img, face, center-int(math.Round(textWidth(face, text)/2)), y, c, text)
}

func textWidth(face font.Face, text string) float64 {
	return float64(font.MeasureString(face, text)) / 64
}

func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA, width int) {
	if width < 1 {
		width = 1
	}
	dx, dy := abs(x1-x0), -abs(y1-y0)
	sx, sy := -1, -1
	if x0 < x1 {
		sx = 1
	}
	if y0 < y1 {
		sy = 1
	}
	err := dx + dy
	for {
		fillRect(img, image.Rect(x0-width/2, y0-width/2, x0-width/2+width, y0-width/2+width), c)
		if x0 == x1 && y0 == y1 {
			break
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

func drawDashedLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA, width, dash int) {
	if dash < 1 {
		dash = 1
	}
	length := int(math.Hypot(float64(x1-x0), float64(y1-y0)))
	if length == 0 {
		fillRect(img, image.Rect(x0, y0, x0+width, y0+width), c)
		return
	}
	steps := length / dash
	for step := 0; step <= steps; step++ {
		start := float64(step * dash)
		end := math.Min(float64(length), start+float64(dash)/1.7)
		ax := x0 + int(math.Round(float64(x1-x0)*start/float64(length)))
		ay := y0 + int(math.Round(float64(y1-y0)*start/float64(length)))
		bx := x0 + int(math.Round(float64(x1-x0)*end/float64(length)))
		by := y0 + int(math.Round(float64(y1-y0)*end/float64(length)))
		drawLine(img, ax, ay, bx, by, c, width)
	}
}

func drawCircle(img *image.RGBA, cx, cy, radius int, c color.RGBA) {
	for y := -radius; y <= radius; y++ {
		for x := -radius; x <= radius; x++ {
			if x*x+y*y <= radius*radius {
				img.SetRGBA(cx+x, cy+y, c)
			}
		}
	}
}

func fillRect(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	draw.Draw(img, rect.Intersect(img.Bounds()), image.NewUniform(c), image.Point{}, draw.Src)
}

func strokeRect(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	drawLine(img, rect.Min.X, rect.Min.Y, rect.Max.X-1, rect.Min.Y, c, 1)
	drawLine(img, rect.Max.X-1, rect.Min.Y, rect.Max.X-1, rect.Max.Y-1, c, 1)
	drawLine(img, rect.Max.X-1, rect.Max.Y-1, rect.Min.X, rect.Max.Y-1, c, 1)
	drawLine(img, rect.Min.X, rect.Max.Y-1, rect.Min.X, rect.Min.Y, c, 1)
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
