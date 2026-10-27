// badapple: map the Bad Apple!! PV onto the Mini 3 Pro LED clock (4 seven-segment
// digits + colon) and the 16-LED ring. Runs on the Mac.
//
//	go run . data <video> <dir>                -> <dir>/frames.bin, <dir>/audio.mp3
//	go run . preview <video> <out.mp4>         -> side-by-side preview with audio
//	go run . scheme <video> <t> <out.png>      -> one frame with the segment regions
//	go run . analyze <video>                   -> beat times, s
//
// The ring flashes white on each beat and is red during the "screech" (two tones near
// 3 and 6 kHz, one per bar) at 1:25-1:50; ALARMS="a-b,c-d" overrides those times.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	W, H = 480, 360
	FPS  = 30
	SR   = 22050
)

// Screech times in the PV (s), found by the 3 + 6 kHz band energy; the lower ones
// (5.5 kHz, every fourth bar) are shorter.
const screech = "85.71-86.13,87.43-87.88,89.18-89.63,90.93-91.16,92.65-93.10,94.40-94.83," +
	"96.13-96.55,97.88-98.10,99.60-100.08,101.35-101.80,103.10-103.53,104.82-105.05," +
	"106.57-107.00,108.32-108.75,110.05-110.47"

// Segment regions inside one digit cell (fractions of the cell: x0, y0, x1, y1).
var segRect = map[byte][4]float64{
	'A': {0.20, 0.00, 0.80, 0.14},
	'B': {0.72, 0.08, 1.00, 0.50},
	'C': {0.72, 0.50, 1.00, 0.92},
	'D': {0.20, 0.86, 0.80, 1.00},
	'E': {0.00, 0.50, 0.28, 0.92},
	'F': {0.00, 0.08, 0.28, 0.50},
	'G': {0.20, 0.43, 0.80, 0.57},
}

const segOrder = "ABCDEFG"

// Colon dots sampled at the frame centre.
var colonRect = map[string][4]float64{
	"DH": {0.46, 0.28, 0.54, 0.40},
	"DL": {0.46, 0.60, 0.54, 0.72},
}

type frameSegs struct {
	digits [4][7]uint8 // A..G per digit, 0..255
	dh, dl uint8
}

func mean(g []byte, x0, y0, x1, y1 int) float64 {
	var s, n int
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			s += int(g[y*W+x])
			n++
		}
	}
	return float64(s) / float64(n)
}

func level(m float64) uint8 {
	v := (m - 40) / (215 - 40)
	v = math.Max(0, math.Min(1, v))
	return uint8(v*255 + 0.5)
}

func cellRect(d int, r [4]float64) (int, int, int, int) {
	cw := W / 4
	return d*cw + int(r[0]*float64(cw)), int(r[1] * H), d*cw + int(r[2]*float64(cw)), int(r[3] * H)
}

func segments(g []byte) frameSegs {
	var f frameSegs
	for d := 0; d < 4; d++ {
		for i := 0; i < 7; i++ {
			x0, y0, x1, y1 := cellRect(d, segRect[segOrder[i]])
			f.digits[d][i] = level(mean(g, x0, y0, x1, y1))
		}
	}
	for k, r := range colonRect {
		v := level(mean(g, int(r[0]*W), int(r[1]*H), int(r[2]*W), int(r[3]*H)))
		if k == "DH" {
			f.dh = v
		} else {
			f.dl = v
		}
	}
	return f
}

func ffmpegOut(args ...string) (io.ReadCloser, *exec.Cmd) {
	c := exec.Command("ffmpeg", append([]string{"-v", "error"}, args...)...)
	out, err := c.StdoutPipe()
	if err != nil {
		panic(err)
	}
	if err := c.Start(); err != nil {
		panic(err)
	}
	return out, c
}

// --- audio: onsets (beats) and per-frame energy -------------------------------

func readAudio(video string) []float64 {
	r, c := ffmpegOut("-i", video, "-ac", "1", "-ar", strconv.Itoa(SR), "-f", "s16le", "-")
	br := bufio.NewReader(r)
	var s []float64
	for {
		var v int16
		if err := binary.Read(br, binary.LittleEndian, &v); err != nil {
			break
		}
		s = append(s, float64(v)/32768)
	}
	c.Wait()
	return s
}

// One-pole low-pass, then energy per 1/FPS hop; onset = positive energy rise
// above a moving threshold, at least minGap apart.
func onsets(s []float64) (beats []float64, energy []float64) {
	hop := SR / FPS
	a := math.Exp(-2 * math.Pi * 150 / SR)
	var lp float64
	low := make([]float64, len(s))
	for i, x := range s {
		lp = a*lp + (1-a)*x
		low[i] = lp
	}
	n := len(s) / hop
	energy = make([]float64, n)
	for i := 0; i < n; i++ {
		var e float64
		for _, x := range low[i*hop : (i+1)*hop] {
			e += x * x
		}
		energy[i] = math.Sqrt(e / float64(hop))
	}
	flux := make([]float64, n)
	for i := 1; i < n; i++ {
		flux[i] = math.Max(0, energy[i]-energy[i-1])
	}
	const win = 15 // 0.5 s
	last := -1e9
	for i := 1; i < n-1; i++ {
		lo, hi := max(0, i-win), min(n, i+win)
		var m float64
		for _, v := range flux[lo:hi] {
			m += v
		}
		m /= float64(hi - lo)
		t := float64(i) / FPS
		if flux[i] > 2.2*m && flux[i] > 0.004 && flux[i] >= flux[i-1] && flux[i] >= flux[i+1] && t-last > 0.25 {
			beats = append(beats, t)
			last = t
		}
	}
	return
}

// --- ring state per frame -----------------------------------------------------

type ringState struct{ r, g, b uint8 }

func ringFor(t float64, beats []float64, alarms [][2]float64) ringState {
	for _, a := range alarms {
		if t >= a[0] && t < a[1] {
			return ringState{255, 0, 0}
		}
	}
	i := sort.SearchFloat64s(beats, t+1e-9) - 1
	if i >= 0 && t-beats[i] < 0.09 { // white flash ~3 frames, then black
		return ringState{255, 255, 255}
	}
	return ringState{}
}

func parseAlarms(s string) [][2]float64 {
	var out [][2]float64
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		ab := strings.Split(p, "-")
		a, _ := strconv.ParseFloat(ab[0], 64)
		b, _ := strconv.ParseFloat(ab[1], 64)
		out = append(out, [2]float64{a, b})
	}
	return out
}

// --- drawing ------------------------------------------------------------------

func fill(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func segColor(v uint8) color.RGBA {
	if v == 0 {
		return color.RGBA{28, 28, 32, 255} // unlit segment outline
	}
	return color.RGBA{v, v, v, 255}
}

// Draw a 7-segment digit with its top-left at (ox, oy), size sw x sh.
func drawDigit(img *image.RGBA, ox, oy, sw, sh int, s [7]uint8) {
	t := sw / 6
	half := sh / 2
	bars := [7][4]int{
		{t, 0, sw - t, t},                   // A
		{sw - t, t, sw, half},               // B
		{sw - t, half, sw, sh - t},          // C
		{t, sh - t, sw - t, sh},             // D
		{0, half, t, sh - t},                // E
		{0, t, t, half},                     // F
		{t, half - t/2, sw - t, half + t/2}, // G
	}
	for i, b := range bars {
		fill(img, ox+b[0]+1, oy+b[1]+1, ox+b[2]-1, oy+b[3]-1, segColor(s[i]))
	}
}

func drawPanel(img *image.RGBA, ox, oy int, f frameSegs, ring ringState) {
	// ring of 16 LEDs around the clock
	cx, cy, R := ox+230, oy+180, 165
	for i := 0; i < 16; i++ {
		ang := 2*math.Pi*float64(i)/16 - math.Pi/2
		x, y := cx+int(float64(R)*math.Cos(ang)), cy+int(float64(R)*math.Sin(ang))
		c := color.RGBA{ring.r, ring.g, ring.b, 255}
		if ring == (ringState{}) {
			c = color.RGBA{24, 24, 28, 255}
		}
		for dy := -10; dy <= 10; dy++ {
			for dx := -10; dx <= 10; dx++ {
				if dx*dx+dy*dy <= 100 {
					img.SetRGBA(x+dx, y+dy, c)
				}
			}
		}
	}
	// clock: 4 digits, colon between 2 and 3
	sw, sh, gap := 52, 104, 16
	x := cx - (4*sw+3*gap+20)/2
	y := cy - sh/2
	for d := 0; d < 4; d++ {
		drawDigit(img, x, y, sw, sh, f.digits[d])
		x += sw + gap
		if d == 1 {
			fill(img, x+2, y+sh/3-5, x+12, y+sh/3+5, segColor(f.dh))
			fill(img, x+2, y+2*sh/3-5, x+12, y+2*sh/3+5, segColor(f.dl))
			x += 20
		}
	}
}

func gray2rgba(g []byte, img *image.RGBA, ox, oy, scale int) {
	for y := 0; y < H/scale; y++ {
		for x := 0; x < W/scale; x++ {
			v := g[(y*scale)*W+x*scale]
			img.SetRGBA(ox+x, oy+y, color.RGBA{v, v, v, 255})
		}
	}
}

func overlayRegions(img *image.RGBA, ox, oy, scale int) {
	line := func(x0, y0, x1, y1 int, c color.RGBA) {
		for x := x0; x <= x1; x++ {
			img.SetRGBA(ox+x/scale, oy+y0/scale, c)
			img.SetRGBA(ox+x/scale, oy+y1/scale, c)
		}
		for y := y0; y <= y1; y++ {
			img.SetRGBA(ox+x0/scale, oy+y/scale, c)
			img.SetRGBA(ox+x1/scale, oy+y/scale, c)
		}
	}
	cols := []color.RGBA{{255, 80, 80, 255}, {80, 200, 255, 255}, {120, 255, 120, 255}, {255, 200, 60, 255}}
	for d := 0; d < 4; d++ {
		for i := 0; i < 7; i++ {
			x0, y0, x1, y1 := cellRect(d, segRect[segOrder[i]])
			line(x0, y0, x1-1, y1-1, cols[d])
		}
	}
	for _, r := range colonRect {
		line(int(r[0]*W), int(r[1]*H), int(r[2]*W), int(r[3]*H), color.RGBA{255, 0, 255, 255})
	}
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: analyze|preview|scheme|data ...")
		os.Exit(2)
	}
	video := os.Args[2]
	alarms := parseAlarms(screech)
	if s := os.Getenv("ALARMS"); s != "" {
		alarms = parseAlarms(s)
	}
	switch os.Args[1] {
	case "analyze":
		beats, _ := onsets(readAudio(video))
		for _, b := range beats {
			fmt.Printf("%.3f\n", b)
		}
		fmt.Fprintf(os.Stderr, "%d beats\n", len(beats))

	case "scheme":
		t := os.Args[3]
		r, c := ffmpegOut("-ss", t, "-i", video, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-")
		g, _ := io.ReadAll(r)
		c.Wait()
		img := image.NewRGBA(image.Rect(0, 0, W+480, H))
		fill(img, 0, 0, W+480, H, color.RGBA{0, 0, 0, 255})
		gray2rgba(g, img, 0, 0, 1)
		overlayRegions(img, 0, 0, 1)
		drawPanel(img, W+10, 0, segments(g), ringState{})
		out, _ := os.Create(os.Args[4])
		png.Encode(out, img)
		out.Close()

	case "preview", "data":
		beats, _ := onsets(readAudio(video))
		r, c := ffmpegOut("-i", video, "-f", "rawvideo", "-pix_fmt", "gray", "-r", strconv.Itoa(FPS), "-")
		br := bufio.NewReaderSize(r, W*H)
		g := make([]byte, W*H)
		if os.Args[1] == "data" {
			// One record per frame: 28 digit segments, DH, DL, ring r, g, b (33 bytes).
			dir := os.Args[3]
			os.MkdirAll(dir, 0o755)
			mp3 := exec.Command("ffmpeg", "-v", "error", "-y", "-i", video, "-vn",
				"-ar", "48000", "-ac", "2", "-c:a", "libmp3lame", "-b:a", "160k", dir+"/audio.mp3")
			mp3.Stderr = os.Stderr
			if err := mp3.Run(); err != nil {
				panic(err)
			}
			out, _ := os.Create(dir + "/frames.bin")
			bw := bufio.NewWriter(out)
			for n := 0; ; n++ {
				if _, err := io.ReadFull(br, g); err != nil {
					break
				}
				f := segments(g)
				for d := 0; d < 4; d++ {
					bw.Write(f.digits[d][:])
				}
				rs := ringFor(float64(n)/FPS, beats, alarms)
				bw.Write([]byte{f.dh, f.dl, rs.r, rs.g, rs.b})
			}
			bw.Flush()
			out.Close()
			c.Wait()
			return
		}
		ow, oh := 240+480, 360
		enc := exec.Command("ffmpeg", "-v", "error", "-y",
			"-f", "rawvideo", "-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", ow, oh), "-r", strconv.Itoa(FPS), "-i", "-",
			"-i", video, "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "23",
			"-c:a", "aac", "-shortest", os.Args[3])
		in, _ := enc.StdinPipe()
		enc.Stderr = os.Stderr
		if err := enc.Start(); err != nil {
			panic(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, ow, oh))
		for n := 0; ; n++ {
			if _, err := io.ReadFull(br, g); err != nil {
				break
			}
			fill(img, 0, 0, ow, oh, color.RGBA{0, 0, 0, 255})
			gray2rgba(g, img, 0, 90, 2)
			drawPanel(img, 240, 0, segments(g), ringFor(float64(n)/FPS, beats, alarms))
			in.Write(img.Pix)
		}
		in.Close()
		enc.Wait()
		c.Wait()
	}
}
