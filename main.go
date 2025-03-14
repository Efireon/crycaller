package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	ui "github.com/gizak/termui/v3"
	"github.com/gizak/termui/v3/widgets"
)

// ===========================================================
// CONFIGURATION, MODEL, LOG BUFFER, TYPES
// ===========================================================

type Config struct {
	BackgroundScripts  []ScriptConfig `json:"background_scripts"`
	InteractiveScripts []ScriptConfig `json:"interactive_scripts"`
}

type ScriptConfig struct {
	Path      string `json:"path"`
	Args      string `json:"args"`
	Type      string `json:"type"`                 // "script" или "binary"
	MaxLogs   int    `json:"max_logs,omitempty"`   // по умолчанию – 5
	Output    bool   `json:"output"`               // если true, вывод будет показан в правой части экрана
	OutputRes string `json:"output_res,omitempty"` // формат "HxW", например "5x40", "5x*" или "5xS"
}

type ScriptStatus int

const (
	StatusWaiting ScriptStatus = iota
	StatusRunning
	StatusPassed
	StatusFailed
)

func (s ScriptStatus) String() string {
	switch s {
	case StatusWaiting:
		return "WAITING"
	case StatusRunning:
		return "RUNNING"
	case StatusPassed:
		return "PASSED"
	case StatusFailed:
		return "FAILED"
	}
	return "???"
}

func statusToString(code int) string {
	if code == 0 {
		return "PASSED"
	}
	return fmt.Sprintf("FAILED(exit=%d)", code)
}

var globalConfig *Config

// --- Log Buffer

type scriptLog struct {
	lines []string
	max   int
	mu    sync.Mutex
}

func newScriptLog(max int) *scriptLog {
	return &scriptLog{
		lines: make([]string, 0, max),
		max:   max,
	}
}

func (sl *scriptLog) append(line string) {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	if len(sl.lines) >= sl.max {
		sl.lines = sl.lines[1:]
	}
	sl.lines = append(sl.lines, line)
}

func (sl *scriptLog) lastLines() []string {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	out := make([]string, len(sl.lines))
	copy(out, sl.lines)
	return out
}

// --- Types: BgScript и IntScript

type BgScript struct {
	Path       string
	Args       string
	Type       string // "script" или "binary"
	Status     ScriptStatus
	Code       int
	Log        *scriptLog
	FullOutput string

	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration

	MaxLogs int

	Output    bool // из конфига
	OutHeight int  // высота блока вывода (строк)
	OutWidth  int  // ширина блока вывода (если 0 – авто, если -1 – полная ширина)

	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func (b *BgScript) Start(wg *sync.WaitGroup, notifyFn func()) {
	defer wg.Done()
	b.Status = StatusRunning
	b.StartTime = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	var cmd *exec.Cmd
	args := parseArgs(b.Args)
	if b.Type == "script" {
		cmd = exec.CommandContext(ctx, "bash", append([]string{b.Path}, args...)...)
	} else if b.Type == "binary" {
		cmd = exec.CommandContext(ctx, b.Path, args...)
	} else {
		log.Printf("Unknown script type for path %s: %s", b.Path, b.Type)
		b.Status = StatusFailed
		b.Code = -1
		notifyFn()
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("Error obtaining stdout for %s: %v", b.Path, err)
		b.Status = StatusFailed
		b.Code = -1
		notifyFn()
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("Error obtaining stderr for %s: %v", b.Path, err)
		b.Status = StatusFailed
		b.Code = -1
		notifyFn()
		return
	}
	b.cmd = cmd
	if err := cmd.Start(); err != nil {
		log.Printf("Error starting command %s: %v", b.Path, err)
		b.Status = StatusFailed
		b.Code = -1
		notifyFn()
		return
	}
	var wgIO sync.WaitGroup
	wgIO.Add(2)
	go func() {
		defer wgIO.Done()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			b.Log.append(line)
			if b.Output {
				b.FullOutput += line + "\n"
			}
			notifyFn()
		}
		if err := sc.Err(); err != nil {
			log.Printf("Error reading stdout for %s: %v", b.Path, err)
		}
	}()
	go func() {
		defer wgIO.Done()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			b.Log.append(line)
			if b.Output {
				b.FullOutput += line + "\n"
			}
			notifyFn()
		}
		if err := sc.Err(); err != nil {
			log.Printf("Error reading stderr for %s: %v", b.Path, err)
		}
	}()
	wgIO.Wait()
	if err2 := cmd.Wait(); err2 != nil {
		if exitErr, ok := err2.(*exec.ExitError); ok {
			b.Code = exitErr.ExitCode()
		} else {
			b.Code = -1
		}
		b.Status = StatusFailed
	} else {
		b.Code = 0
		b.Status = StatusPassed
	}
	b.EndTime = time.Now()
	b.Duration = b.EndTime.Sub(b.StartTime)
	notifyFn()
}

func (b *BgScript) Stop() {
	if b.cancel != nil {
		b.cancel()
	}
}

type IntScript struct {
	Path       string
	Args       string
	Type       string // "script" или "binary"
	Status     ScriptStatus
	Code       int
	Log        *scriptLog // опционально
	FullOutput string
	StartTime  time.Time
	EndTime    time.Time
	Duration   time.Duration
	MaxLogs    int
	Output     bool // из конфига
	OutHeight  int  // высота блока вывода
	OutWidth   int  // ширина блока вывода (если 0 – авто, если -1 – полная ширина)

	cmd          *exec.Cmd
	pty          *os.File // псевдотерминал для процесса
	screenBuffer string   // ограниченный вывод для окна
}

func (i *IntScript) Start(wg *sync.WaitGroup, notifyFn func()) {
	defer wg.Done()
	i.Status = StatusRunning
	i.StartTime = time.Now()
	var cmd *exec.Cmd
	args := parseArgs(i.Args)
	if i.Type == "script" {
		cmd = exec.CommandContext(context.Background(), "bash", append([]string{i.Path}, args...)...)
	} else if i.Type == "binary" {
		cmd = exec.CommandContext(context.Background(), i.Path, args...)
	} else {
		log.Printf("Unknown script type for path %s: %s", i.Path, i.Type)
		i.Status = StatusFailed
		i.Code = -1
		notifyFn()
		return
	}
	i.cmd = cmd
	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("Error starting pty for %s: %v", i.Path, err)
		i.Status = StatusFailed
		i.Code = -1
		notifyFn()
		return
	}
	i.pty = ptmx
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				text := string(buf[:n])
				i.screenBuffer += text
				if i.Output {
					i.FullOutput += text
				}
				notifyFn()
			}
			if err != nil {
				break
			}
		}
	}()
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			i.Code = exitErr.ExitCode()
		} else {
			i.Code = -1
		}
		i.Status = StatusFailed
	} else {
		i.Code = 0
		i.Status = StatusPassed
	}
	i.EndTime = time.Now()
	i.Duration = i.EndTime.Sub(i.StartTime)
	ptmx.Close()
	notifyFn()
}

// -----------------------------------------------------------
// РЕНДЕРЕР: Функции для отрисовки плиток и группировки
// -----------------------------------------------------------

// centerText выравнивает строку по центру заданной ширины.
func centerText(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	padding := (width - len(s)) / 2
	return strings.Repeat(" ", padding) + s + strings.Repeat(" ", width-len(s)-padding)
}

// drawTile рисует плитку с рамкой, где заголовок (title) вписывается как первая строка,
// а для содержимого используется (height-1) строк.
func drawTile(title, content string, height, width int) string {
	if height < 3 {
		height = 3
	}
	internalHeight := height - 2
	// Формируем первую строку: заголовок, центрированный по ширине
	header := centerText(title, width)
	// Разбиваем содержимое на строки и подгоняем до внутренней высоты
	lines := strings.Split(content, "\n")
	if len(lines) > internalHeight-1 {
		lines = lines[len(lines)-(internalHeight-1):]
	} else {
		for i := len(lines); i < internalHeight-1; i++ {
			lines = append(lines, "")
		}
	}
	// Дополняем каждую строку до ширины
	for i, line := range lines {
		if len(line) < width {
			lines[i] = line + strings.Repeat(" ", width-len(line))
		} else if len(line) > width {
			lines[i] = line[:width]
		}
	}
	// Собираем внутреннее содержимое: заголовок + содержимое
	inner := header + "\n" + strings.Join(lines, "\n")
	topBorder := "┌" + strings.Repeat("─", width) + "┐"
	bottomBorder := "└" + strings.Repeat("─", width) + "┘"
	return topBorder + "\n" + inner + "\n" + bottomBorder
}

// blockSize вычисляет фактическую ширину плитки (максимум по всем строкам).

// -----------------------------------------------------------
// PARSE ARGS и parseOutputRes
// -----------------------------------------------------------

func (sr *splitReader) Read(p []byte) (int, error) {
	if len(sr.r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, sr.r)
	sr.r = sr.r[n:]
	return n, nil
}

// -----------------------------------------------------------
// РЕНДЕРЕР НА TERMUI (ВСЕГО ПОЛНЫЙ)
// -----------------------------------------------------------

type model struct {
	bgScripts    []*BgScript
	intScripts   []*IntScript
	mode         uiMode
	quitting     bool
	exitCode     int
	programStart time.Time
	// Для обновления UI в нашем рендерере используем поля ширины/высоты из termui
	width, height int
}

type uiMode int

const (
	modeMain uiMode = iota
	modeFinal
)

type (
	doneAllMsg struct{} // все скрипты завершены
	refreshMsg struct{} // перерисовать
)

func computeExitCode(bgs []*BgScript, ints []*IntScript) int {
	for _, s := range bgs {
		if s.Status == StatusFailed {
			return 1
		}
	}
	for _, s := range ints {
		if s.Status == StatusFailed {
			return 1
		}
	}
	return 0
}

func allScriptsDone(bgs []*BgScript, ints []*IntScript) bool {
	for _, s := range bgs {
		if s.Status == StatusWaiting || s.Status == StatusRunning {
			return false
		}
	}
	for _, s := range ints {
		if s.Status == StatusWaiting || s.Status == StatusRunning {
			return false
		}
	}
	return true
}

// Методы модели для формирования строкового содержимого (не изменяйте их – функционал прежний)
func (m *model) viewMain() string {
	title := asciiBannerMain()
	passedPart := m.renderCollapsedPassed()
	failedPart := m.renderCollapsedFailed()
	runningPart := m.renderRunning()
	footer := "\nPress 'q' to exit | 'r' to restart | input is sent to active interactive script\n"
	return strings.Join([]string{
		title,
		"",
		passedPart,
		failedPart,
		runningPart,
		footer,
	}, "\n")
}

func (m *model) renderRunning() string {
	var lines []string
	lines = append(lines, asciiSep("RUNNING TESTS"))
	lines = append(lines, "===> Background:")
	for _, s := range m.bgScripts {
		if s.Status == StatusRunning {
			win := renderEmbeddedWindow(fmt.Sprintf("> %s", s.Path), strings.Join(s.Log.lastLines(), "\n"), s.MaxLogs, s.Status)
			lines = append(lines, win)
		}
	}
	lines = append(lines, "===> Interactive:")
	for _, s := range m.intScripts {
		if s.Status == StatusRunning {
			win := renderEmbeddedWindow(fmt.Sprintf("> %s", s.Path), s.screenBuffer, s.MaxLogs, s.Status)
			lines = append(lines, win)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *model) renderCollapsedPassed() string {
	var names []string
	for _, s := range m.intScripts {
		if s.Status == StatusPassed {
			names = append(names, s.Path)
		}
	}
	for _, s := range m.bgScripts {
		if s.Status == StatusPassed {
			names = append(names, s.Path)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return asciiSep("PASSED (Collapsed)") + "\n" + strings.Join(names, ", ")
}

func (m *model) renderCollapsedFailed() string {
	var names []string
	for _, s := range m.intScripts {
		if s.Status == StatusFailed {
			names = append(names, s.Path)
		}
	}
	for _, s := range m.bgScripts {
		if s.Status == StatusFailed {
			names = append(names, s.Path)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return asciiSep("FAILED (Collapsed)") + "\n" + strings.Join(names, ", ")
}

func asciiBannerFinal() string {
	return "FINAL RESULTS"
}

func statusColor(code int) string {
	if code == 0 {
		return "[PASSED]"
	}
	return fmt.Sprintf("[FAILED(exit=%d)]", code)
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// ===========================================================
// РЕНДЕРЕР ПЛИТОК: новые функции для плиточного вывода (без lipgloss)
// ===========================================================

// centerText выравнивает строку по центру
func centerText(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	pad := (width - len(s)) / 2
	return strings.Repeat(" ", pad) + s + strings.Repeat(" ", width-len(s)-pad)
}

// drawTile рисует плитку с рамкой. Высота (height) включает рамку (то есть минимум 3 строки).
func drawTile(title, content string, height, width int) string {
	if height < 3 {
		height = 3
	}
	internalHeight := height - 2
	// Заголовок
	hdr := centerText(title, width)
	// Содержимое
	lines := strings.Split(content, "\n")
	if len(lines) > internalHeight-1 {
		lines = lines[len(lines)-(internalHeight-1):]
	} else {
		for i := len(lines); i < internalHeight-1; i++ {
			lines = append(lines, "")
		}
	}
	// Дополняем каждую строку до ширины
	for i, line := range lines {
		if len(line) < width {
			lines[i] = padRight(line, width)
		} else if len(line) > width {
			lines[i] = line[:width]
		}
	}
	inner := hdr + "\n" + strings.Join(lines, "\n")
	top := "┌" + strings.Repeat("─", width) + "┐"
	bot := "└" + strings.Repeat("─", width) + "┘"
	return top + "\n" + inner + "\n" + bot
}

// blockSize вычисляет максимальную ширину плитки (по всем строкам)
func blockSize(tile string) int {
	lines := strings.Split(tile, "\n")
	max := 0
	for _, l := range lines {
		if len(l) > max {
			max = len(l)
		}
	}
	return max
}

// groupTiles группирует плитки в ряды, так чтобы каждая плитка целиком помещалась.
func groupTiles(tiles []string, availableWidth int) string {
	var rows []string
	var currentRow []string
	currentWidth := 0
	sep := "  "
	sepWidth := len(sep)

	for _, t := range tiles {
		tw := blockSize(t)
		if len(currentRow) == 0 {
			currentRow = append(currentRow, t)
			currentWidth = tw
		} else {
			if currentWidth+sepWidth+tw <= availableWidth {
				currentRow = append(currentRow, t)
				currentWidth += sepWidth + tw
			} else {
				rows = append(rows, strings.Join(currentRow, sep))
				currentRow = []string{t}
				currentWidth = tw
			}
		}
	}
	if len(currentRow) > 0 {
		rows = append(rows, strings.Join(currentRow, sep))
	}
	return strings.Join(rows, "\n\n")
}

// renderOutputPanel собирает плитки для скриптов с Output==true и группирует их.
func renderOutputPanel(bg []*BgScript, intS []*IntScript, availableWidth int) string {
	var tiles []string
	for _, s := range bg {
		if s.Output {
			h, w := s.OutHeight, s.OutWidth
			if h == 0 {
				h = 10
			}
			tile := drawTile(s.Path, s.FullOutput, h, w)
			tiles = append(tiles, tile)
		}
	}
	for _, s := range intS {
		if s.Output {
			h, w := s.OutHeight, s.OutWidth
			if h == 0 {
				h = 10
			}
			tile := drawTile(s.Path, s.FullOutput, h, w)
			tiles = append(tiles, tile)
		}
	}
	return groupTiles(tiles, availableWidth)
}

// ===========================================================
// PARSE ARGS и parseOutputRes
// ===========================================================

func parseArgs(s string) []string {
	var result []string
	sc := bufio.NewScanner(&splitReader{r: s})
	sc.Split(bufio.ScanWords)
	for sc.Scan() {
		result = append(result, sc.Text())
	}
	return result
}

type splitReader struct{ r string }

func (sr *splitReader) Read(p []byte) (int, error) {
	if len(sr.r) == 0 {
		return 0, io.EOF
	}
	n := copy(p, sr.r)
	sr.r = sr.r[n:]
	return n, nil
}

// ===========================================================
// РЕНДЕРЕР НА TERMUI
// ===========================================================

type appModel struct {
	bgScripts     []*BgScript
	intScripts    []*IntScript
	mode          uiMode
	quitting      bool
	exitCode      int
	programStart  time.Time
	width, height int
}

func (m *appModel) updateDimensions() {
	w, h := ui.TerminalDimensions()
	m.width = w
	m.height = h
}

func drawUI(m *appModel) {
	m.updateDimensions()

	// ЛЕВАЯ ПАНЕЛЬ: основной вывод
	leftPara := widgets.NewParagraph()
	leftPara.Text = m.viewMain()
	leftPara.Title = "Основной вывод"
	leftPara.Border = true
	// Пусть левая панель занимает 40% ширины
	leftWidth := m.width * 40 / 100
	leftPara.SetRect(0, 0, leftWidth, m.height)

	// ПРАВАЯ ПАНЕЛЬ: вывод скриптов (плитки)
	rightText := renderOutputPanel(m.bgScripts, m.intScripts, m.width-leftWidth-4)
	rightPara := widgets.NewParagraph()
	rightPara.Text = rightText
	rightPara.Title = "Вывод скриптов"
	rightPara.Border = true
	rightPara.SetRect(leftWidth+3, 0, m.width, m.height)

	// Разделитель – вертикальная линия
	divider := widgets.NewParagraph()
	divider.Text = strings.Repeat("│", m.height)
	divider.Border = false
	divider.SetRect(leftWidth, 0, leftWidth+3, m.height)

	ui.Render(leftPara, divider, rightPara)
}

func (m *appModel) viewMain() string {
	title := asciiBannerMain()
	passedPart := m.renderCollapsedPassed()
	failedPart := m.renderCollapsedFailed()
	runningPart := m.renderRunning()
	footer := "\nPress 'q' to exit | 'r' to restart | input sent to interactive script"
	return strings.Join([]string{
		title,
		"",
		passedPart,
		failedPart,
		runningPart,
		footer,
	}, "\n")
}

func (m *appModel) renderRunning() string {
	var lines []string
	lines = append(lines, asciiSep("RUNNING TESTS"))
	lines = append(lines, "===> Background:")
	for _, s := range m.bgScripts {
		if s.Status == StatusRunning {
			win := renderEmbeddedWindow(fmt.Sprintf("> %s", s.Path), strings.Join(s.Log.lastLines(), "\n"), s.MaxLogs, s.Status)
			lines = append(lines, win)
		}
	}
	lines = append(lines, "===> Interactive:")
	for _, s := range m.intScripts {
		if s.Status == StatusRunning {
			win := renderEmbeddedWindow(fmt.Sprintf("> %s", s.Path), s.screenBuffer, s.MaxLogs, s.Status)
			lines = append(lines, win)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *appModel) renderCollapsedPassed() string {
	var names []string
	for _, s := range m.intScripts {
		if s.Status == StatusPassed {
			names = append(names, s.Path)
		}
	}
	for _, s := range m.bgScripts {
		if s.Status == StatusPassed {
			names = append(names, s.Path)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return asciiSep("PASSED (Collapsed)") + "\n" + strings.Join(names, ", ")
}

func (m *appModel) renderCollapsedFailed() string {
	var names []string
	for _, s := range m.intScripts {
		if s.Status == StatusFailed {
			names = append(names, s.Path)
		}
	}
	for _, s := range m.bgScripts {
		if s.Status == StatusFailed {
			names = append(names, s.Path)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return asciiSep("FAILED (Collapsed)") + "\n" + strings.Join(names, ", ")
}

func asciiBannerMain() string {
	return "LIGHT IT ALL UP"
}

func asciiSep(label string) string {
	return "==== " + label + " ===="
}

// Для простоты оставляем asciiBannerFinal() не используемым в текущем UI

// renderEmbeddedWindow – старая функция для отрисовки окна (с рамкой) для основного вывода.
// Здесь оставляем её простым.
func renderEmbeddedWindow(title, content string, height int, status ScriptStatus) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[len(lines)-height:]
	} else {
		for i := len(lines); i < height; i++ {
			lines = append(lines, "")
		}
	}
	inner := strings.Join(lines, "\n")
	return title + "\n" + inner
}

// ===========================================================
// MAIN
// ===========================================================

func main() {
	// Загружаем конфигурацию
	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("Error reading config.json: %v", err)
	}
	if cfg == nil {
		log.Fatalf("config.json is empty или invalid")
	}
	globalConfig = cfg

	// Создаем объекты скриптов
	var bgScripts []*BgScript
	for _, sc := range cfg.BackgroundScripts {
		maxLogs := 5
		if sc.MaxLogs > 0 {
			maxLogs = sc.MaxLogs
		}
		outH, outW := 0, 0
		if sc.OutputRes != "" {
			outH, outW = parseOutputRes(sc.OutputRes)
		}
		bgScripts = append(bgScripts, &BgScript{
			Path:       sc.Path,
			Args:       sc.Args,
			Type:       sc.Type,
			Status:     StatusWaiting,
			Code:       -1,
			Log:        newScriptLog(maxLogs),
			FullOutput: "",
			MaxLogs:    maxLogs,
			Output:     sc.Output,
			OutHeight:  outH,
			OutWidth:   outW,
		})
	}

	var intScripts []*IntScript
	for _, sc := range cfg.InteractiveScripts {
		maxLogs := 5
		if sc.MaxLogs > 0 {
			maxLogs = sc.MaxLogs
		}
		outH, outW := 0, 0
		if sc.OutputRes != "" {
			outH, outW = parseOutputRes(sc.OutputRes)
		}
		intScripts = append(intScripts, &IntScript{
			Path:       sc.Path,
			Args:       sc.Args,
			Type:       sc.Type,
			Status:     StatusWaiting,
			Code:       -1,
			Log:        newScriptLog(maxLogs),
			FullOutput: "",
			MaxLogs:    maxLogs,
			Output:     sc.Output,
			OutHeight:  outH,
			OutWidth:   outW,
		})
	}

	// Создаем модель приложения
	app := &appModel{
		bgScripts:    bgScripts,
		intScripts:   intScripts,
		mode:         modeMain,
		exitCode:     0,
		programStart: time.Now(),
	}

	// Запускаем терминальный UI
	if err := ui.Init(); err != nil {
		log.Fatalf("failed to initialize termui: %v", err)
	}
	defer ui.Close()

	// Запускаем скрипты
	var wg sync.WaitGroup
	notifyFn := func() { ui.Clear() }
	for _, bg := range bgScripts {
		wg.Add(1)
		go bg.Start(&wg, notifyFn)
	}
	for _, is := range intScripts {
		wg.Add(1)
		go is.Start(&wg, notifyFn)
	}

	// Запускаем цикл обновления UI
	uiEvents := ui.PollEvents()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case e := <-uiEvents:
			switch e.ID {
			case "q", "<C-c>":
				break loop
			case "r":
				// перезапуск
				ui.Close()
				exec.Command(os.Args[0]).Start()
				os.Exit(0)
			}
		case <-ticker.C:
			drawUI(app)
		}
	}
}

func loadConfig(fname string) (*Config, error) {
	data, err := os.ReadFile(fname)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parseOutputRes(res string) (int, int) {
	parts := strings.Split(res, "x")
	if len(parts) != 2 {
		return 10, 40
	}
	if parts[1] == "*" {
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 10, 40
		}
		return h, -1
	}
	if strings.ToUpper(parts[1]) == "S" {
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 10, 40
		}
		return h, 0
	}
	h, err1 := strconv.Atoi(parts[0])
	w, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 10, 40
	}
	return h, w
}
