// badapple player, on the speaker (YAM plugin, started by its service API):
// plays data/audio.mp3 and draws data/frames.bin on the LED clock and the ring.
//
// Before the show: pause the nightlight, mute Alice's microphones, remember what
// the clock and the ring show. After it (end of song or SIGTERM from "stop"):
// give everything back. After the song it waits for "stop" instead of exiting,
// so YAM does not start it again.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	fps    = 30
	recLen = 33 // 28 digit segments (A..G per digit), DH, DL, ring r, g, b
	leds   = "/sys/class/leds/"
	yam    = "http://127.0.0.1:8099/api/plugins"
	run    = "/run/badapple"
)

// Audio reaches the speaker later than mpg123 starts (decoder + dmix buffer).
const defaultOffset = 0.20

type led struct {
	f    *os.File
	last int
}

func openLED(name string) *led {
	f, err := os.OpenFile(leds+name+"/brightness", os.O_WRONLY, 0)
	if err != nil {
		return nil
	}
	return &led{f: f, last: -1}
}

func (l *led) set(v int) {
	if l == nil || v == l.last {
		return
	}
	l.f.WriteAt([]byte(strconv.Itoa(v)), 0)
	l.last = v
}

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

func writeFile(p, s string) { os.WriteFile(p, []byte(s), 0o644) }

// --- clock and ring -------------------------------------------------------------

var clockNames = func() (n []string) {
	for _, s := range "ABCDEFGH" {
		for d := 0; d < 4; d++ {
			n = append(n, fmt.Sprintf("pickle:white:clock%c%d", s, d))
		}
	}
	return append(n, "pickle:white:clockDH", "pickle:white:clockDL", "pickle:white:clockDM",
		"pickle:white:clockDEGREE")
}()

var colors = []string{"red", "green", "blue"}

func ringName(c string, i int) string { return fmt.Sprintf("pickle:%s:ring%d", c, i) }

// Remember the clock brightness, to put it back after the show.
func saveClock() map[string]string {
	m := map[string]string{}
	for _, n := range clockNames {
		m[n] = readFile(leds + n + "/brightness")
	}
	return m
}

func restore(clock map[string]string) {
	for n, v := range clock {
		writeFile(leds+n+"/brightness", v)
	}
	// Writing brightness detached the ring from Alice; give it back.
	for _, c := range colors {
		for i := 0; i < 16; i++ {
			writeFile(leds+ringName(c, i)+"/trigger", "ledfb-ring")
		}
	}
}

// --- nightlight and microphone ------------------------------------------------------

func nightlightOn() bool {
	out, _ := exec.Command("wget", "-q", "-O", "-", yam).Output()
	for _, p := range strings.Split(string(out), `{"manifest"`)[1:] {
		if strings.Contains(p, `"id":"nightlight"`) && strings.Contains(p, `"running":true`) {
			return true
		}
	}
	return false
}

func post(path string) {
	exec.Command("wget", "-q", "-O", "/dev/null", "--post-data", "", yam+path).Run()
}

func muteDevice() string {
	b, _ := os.ReadFile("/proc/bus/input/devices")
	found := false
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, `N: Name="mute"`) {
			found = true
		}
		if found && strings.HasPrefix(l, "H: Handlers=") {
			for _, h := range strings.Fields(strings.TrimPrefix(l, "H: Handlers=")) {
				if strings.HasPrefix(h, "event") {
					return "/dev/input/" + h
				}
			}
		}
	}
	return ""
}

const swMuteDevice = 0x0e

// Microphones are muted when the SW_MUTE_DEVICE switch is on (EVIOCGSW).
func micMuted(dev string) bool {
	f, err := os.Open(dev)
	if err != nil {
		return false
	}
	defer f.Close()
	var bits [8]byte
	req := uintptr(2<<30 | len(bits)<<16 | 'E'<<8 | 0x1b)
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&bits[0])))
	return bits[swMuteDevice/8]&(1<<(swMuteDevice%8)) != 0
}

// Quasar handles a SW_MUTE_DEVICE event written to the "mute" device like the button.
func setMic(dev string, muted bool) {
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()
	v := int32(0)
	if muted {
		v = 1
	}
	ev := func(typ, code uint16, val int32) {
		var b [24]byte // struct input_event: timeval (16), type, code, value
		binary.LittleEndian.PutUint16(b[16:], typ)
		binary.LittleEndian.PutUint16(b[18:], code)
		binary.LittleEndian.PutUint32(b[20:], uint32(val))
		f.Write(b[:])
	}
	ev(5, swMuteDevice, v) // EV_SW
	ev(0, 0, 0)            // SYN_REPORT
}

// --- state, so a killed show can be undone by the next start ---------------------

type state struct {
	nightlight, mic bool
	clock           map[string]string
}

func saveState(s state) {
	var b strings.Builder
	fmt.Fprintf(&b, "nightlight=%v\nmic=%v\n", s.nightlight, s.mic)
	for n, v := range s.clock {
		fmt.Fprintf(&b, "%s=%s\n", n, v)
	}
	os.MkdirAll(run, 0o755)
	writeFile(run+"/state", b.String())
}

func loadState() (state, bool) {
	f, err := os.Open(run + "/state")
	if err != nil {
		return state{}, false
	}
	defer f.Close()
	s := state{clock: map[string]string{}}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "nightlight":
			s.nightlight = v == "true"
		case "mic":
			s.mic = v == "true"
		default:
			s.clock[k] = v
		}
	}
	return s, true
}

func undo(s state, dev string) {
	restore(s.clock)
	if s.mic {
		setMic(dev, false)
	}
	if s.nightlight {
		post("/nightlight/service/start")
	}
	os.Remove(run + "/state")
}

func airplayActive() bool {
	pid := readFile("/run/lightmusic/pid")
	return pid != "" && syscall.Kill(atoi(pid), 0) == nil
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func waitForStop(sig chan os.Signal) {
	<-sig
	os.Exit(0)
}

func main() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	dev := muteDevice()

	// A show killed without cleanup left its state behind.
	if s, ok := loadState(); ok {
		undo(s, dev)
	}
	// Started by yamd at boot, not by a command: do nothing.
	if up := readFile("/proc/uptime"); atoi(strings.Split(up, ".")[0]) < 120 {
		fmt.Println("badapple: just booted, not playing")
		waitForStop(sig)
	}
	if airplayActive() {
		fmt.Println("badapple: AirPlay is playing, not starting")
		waitForStop(sig)
	}

	exe, _ := os.Executable()
	data := filepath.Join(filepath.Dir(exe), "..", "data")
	frames, err := os.ReadFile(filepath.Join(data, "frames.bin"))
	if err != nil || len(frames) < recLen {
		fmt.Println("badapple: no frames:", err)
		waitForStop(sig)
	}
	offset := defaultOffset
	if v, err := strconv.ParseFloat(readFile(filepath.Join(data, "offset")), 64); err == nil {
		offset = v
	}

	s := state{nightlight: nightlightOn()}
	if s.nightlight {
		post("/nightlight/stop") // gives the ring back to Alice first
	}
	if dev != "" && !micMuted(dev) {
		setMic(dev, true)
		s.mic = true
	}
	s.clock = saveClock()
	saveState(s)

	var clock [30]*led // 28 digit segments, DH, DL
	for d := 0; d < 4; d++ {
		for i, c := range "ABCDEFG" {
			clock[d*7+i] = openLED(fmt.Sprintf("pickle:white:clock%c%d", c, d))
		}
	}
	clock[28], clock[29] = openLED("pickle:white:clockDH"), openLED("pickle:white:clockDL")
	for _, n := range []string{"H0", "H1", "H2", "H3", "DM", "DEGREE"} {
		openLED("pickle:white:clock" + n).set(0)
	}
	var ring [3][16]*led
	for c := range colors {
		for i := 0; i < 16; i++ {
			ring[c][i] = openLED(ringName(colors[c], i))
		}
	}

	player := exec.Command("mpg123", "-q", filepath.Join(data, "audio.mp3"))
	if err := player.Start(); err != nil {
		fmt.Println("badapple: mpg123:", err)
		undo(s, dev)
		waitForStop(sig)
	}
	done := make(chan struct{})
	go func() { player.Wait(); close(done) }()
	start := time.Now().Add(time.Duration(offset * float64(time.Second)))

	finish := func() {
		player.Process.Signal(syscall.SIGTERM)
		undo(s, dev)
	}
	tick := time.NewTicker(time.Second / (2 * fps))
	defer tick.Stop()
	n := len(frames) / recLen
	lastFrame := -1
	for {
		select {
		case <-sig:
			finish()
			os.Exit(0)
		case <-done:
			finish()
			fmt.Println("badapple: done, waiting for stop")
			waitForStop(sig)
		case <-tick.C:
		}
		i := int(time.Since(start).Seconds() * fps)
		if i < 0 {
			continue
		}
		if i >= n {
			i = n - 1
		}
		r := frames[i*recLen : (i+1)*recLen]
		// Quasar redraws the time each minute behind our back: rewrite the clock
		// every frame instead of only on change.
		if i != lastFrame {
			for k := 0; k < 30; k++ {
				if clock[k] != nil {
					clock[k].last = -1
					clock[k].set(int(r[k]))
				}
			}
			lastFrame = i
		}
		for c := 0; c < 3; c++ {
			for j := 0; j < 16; j++ {
				ring[c][j].set(int(r[30+c]))
			}
		}
	}
}
