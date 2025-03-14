package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	gc "github.com/rthornton128/goncurses"
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\nOptions:\n", os.Args[0])
		flag.PrintDefaults()
	}
}

// Config хранит группы USB-портов, выбранные группы для тестирования и требуемое число тестов для каждой группы.
type Config struct {
	Motherboard string              `json:"motherboard"`
	Ports       map[string][]string `json:"ports"`       // имя группы -> []USB портов
	Selected    []string            `json:"selected"`    // выбранные группы
	TestCounts  map[string]int      `json:"test_counts"` // требуемое число тестов для каждой группы (статично)
}

var (
	configFile  = flag.String("c", "usb_test.json", "Configuration file")
	quickCheck  = flag.Bool("quick", false, "Immediately enter auto check mode")
	checkSelect = flag.Bool("check-select", false, "Select groups for checking before auto-check mode")
	retestCount = flag.Int("retest", 0, "Number of retest cycles in check mode")
	testMode    = flag.Bool("T", false, "Immediately enter Auto Test mode")
	displayMode = flag.Bool("d", false, "Display currently connected USB devices (non-curses) and exit")
)

// USBDevice описывает устройство, полученное из lsblk.
type USBDevice struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Model string `json:"model"`
	Tran  string `json:"tran"`
}

func main() {
	flag.Parse()

	// Если задан режим отображения USB (-d), то не запускаем curses,
	// а выводим список устройств в стандартный вывод.
	if *displayMode {
		displayUSBModeStd()
		os.Exit(0)
	}

	config := loadConfig()

	stdscr, err := gc.Init()
	if err != nil {
		log.Fatalf("Failed to initialize curses: %v", err)
	}
	defer gc.End()
	stdscr.Keypad(true)
	gc.Echo(false)
	gc.Cursor(0) // скрыть курсор

	// Обработка Ctrl+C.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		gc.End()
		os.Exit(0)
	}()

	// Если задан флаг -T, сразу переходим в режим Auto Test.
	if *testMode {
		if *retestCount > 0 {
			retestMode(stdscr, config, config.Selected)
		} else {
			autoCheckMode(stdscr, config, config.Selected)
		}
		os.Exit(0)
	}

	// Если указан quick-режим.
	if *quickCheck {
		var selected []string
		if len(config.Selected) > 0 {
			selected = config.Selected
		} else if *checkSelect {
			selected, _ = getSelectedGroups(stdscr, config)
		}
		if *retestCount > 0 {
			retestMode(stdscr, config, selected)
		} else {
			autoCheckMode(stdscr, config, selected)
		}
		os.Exit(0)
	}

	// Главное меню (curses-режим).
	for {
		showMainMenu(stdscr)
		gc.FlushInput()
		ch := stdscr.GetChar()
		switch ch {
		case '1':
			portLearningMode(stdscr, config)
		case '2':
			editMode(stdscr, config)
		case '3':
			deleteMode(stdscr, config)
		case '4':
			var selected []string
			if len(config.Selected) > 0 {
				selected = config.Selected
			} else if *checkSelect {
				selected, _ = getSelectedGroups(stdscr, config)
			}
			if *retestCount > 0 {
				retestMode(stdscr, config, selected)
			} else {
				autoCheckMode(stdscr, config, selected)
			}
			os.Exit(0)
		case '5':
			selGroups, testCounts := selectPriorityGroups(stdscr, config)
			if len(selGroups) > 0 {
				config.Selected = selGroups
				config.TestCounts = testCounts
				saveConfig(config)
				showMessage(stdscr, "Priority groups and test counts saved.")
			} else {
				showMessage(stdscr, "No changes made.")
			}
			stdscr.GetChar()
		case 'q', 'Q', 27:
			showMessage(stdscr, "Exiting without saving.")
			stdscr.GetChar()
			return
		default:
			showMessage(stdscr, "Invalid option. Press any key to continue.")
			stdscr.GetChar()
		}
	}
}

// displayUSBModeStd реализует режим -d без использования curses.
// Он периодически (раз в секунду) опрашивает USB-устройства и сравнивает с предыдущим состоянием,
// выводя сообщения о подключении и отключении устройств.
func displayUSBModeStd() {
	prev := make(map[string]USBDevice)
	for {
		currDevices := getUSBDevicesInfo()
		curr := make(map[string]USBDevice)
		for _, dev := range currDevices {
			curr[dev.Name] = dev
		}
		// Определяем новые устройства.
		for name, dev := range curr {
			if _, ok := prev[name]; !ok {
				fmt.Printf("USB connected: /dev/%s - Label: %s, Disk: %s\n", dev.Name, dev.Label, dev.Model)
			}
		}
		// Определяем отключенные устройства.
		for name, dev := range prev {
			if _, ok := curr[name]; !ok {
				fmt.Printf("USB disconnected: /dev/%s - Label: %s, Disk: %s\n", dev.Name, dev.Label, dev.Model)
			}
		}
		prev = curr
		time.Sleep(1 * time.Second)
	}
}

// displayUSBModeStd вызывается, если задан флаг -d.
// Он не использует curses.
func displayUSBModeStdWrapper() {
	displayUSBModeStd()
}

// getUSBDevicesInfo получает информацию о USB-устройствах через lsblk.
// Если поле Label пустое, подставляется "NONAME".
func getUSBDevicesInfo() []USBDevice {
	cmd := exec.Command("lsblk", "-o", "NAME,LABEL,MODEL,TRAN", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	var result struct {
		Blockdevices []USBDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil
	}
	var devices []USBDevice
	for _, dev := range result.Blockdevices {
		if strings.ToLower(dev.Tran) == "usb" {
			if strings.TrimSpace(dev.Label) == "" {
				dev.Label = "NONAME"
			}
			devices = append(devices, dev)
		}
	}
	return devices
}

// showMainMenu выводит главное меню (curses-режим).
func showMainMenu(win *gc.Window) {
	win.Erase()
	win.MovePrint(1, 2, "==== Main Menu ====")
	win.MovePrint(3, 4, "[1] Port learning mode")
	win.MovePrint(4, 4, "[2] Edit USB groups (interactive)")
	win.MovePrint(5, 4, "[3] Delete USB from groups (interactive)")
	win.MovePrint(6, 4, "[4] Auto Test mode")
	win.MovePrint(7, 4, "[5] Set Priority Groups for Testing")
	win.MovePrint(9, 4, "[Q] Quit (without saving)")
	win.MovePrint(11, 2, "Select an option: ")
	win.Refresh()
}

// Port Learning Mode: формирует группы USB-портов.
func portLearningMode(win *gc.Window, config *Config) {
	mobo := getMotherboardID()
	config.Motherboard = mobo
	win.Timeout(100)
	currentGroup := make(map[string]bool)
	groupNumber := 1
	lastUpdate := time.Now()

	for {
		if time.Since(lastUpdate) >= 2*time.Second {
			devices := detectUSBDevices()
			for _, device := range devices {
				portID := getPortID(device)
				if portID == "" {
					continue
				}
				currentGroup[portID] = true
			}
			lastUpdate = time.Now()
		}

		win.Erase()
		win.MovePrint(0, 2, "Port Learning Mode")
		win.MovePrint(1, 2, "Motherboard: "+mobo)
		win.MovePrint(3, 2, "Press [N] to save current group and start a new group.")
		win.MovePrint(4, 2, "Press [S] to save the profile and return to main menu.")
		win.MovePrint(5, 2, "Press [E] to exit without saving current group.")
		line := 7
		win.MovePrint(line, 2, fmt.Sprintf("Current Group %d:", groupNumber))
		line++
		for portID := range currentGroup {
			win.MovePrint(line, 4, portID)
			line++
		}
		win.Refresh()

		ch := win.GetChar()
		switch ch {
		case 'n', 'N':
			if len(currentGroup) > 0 {
				groupName := fmt.Sprintf("usb%d", groupNumber)
				config.Ports[groupName] = mapKeysToSlice(currentGroup)
				groupNumber++
				currentGroup = make(map[string]bool)
				showMessage(win, fmt.Sprintf("Saved group %s.", groupName))
				time.Sleep(1 * time.Second)
				gc.FlushInput()
			} else {
				showMessage(win, "Current group is empty.")
				time.Sleep(1 * time.Second)
				gc.FlushInput()
			}
		case 's', 'S':
			if len(currentGroup) > 0 {
				groupName := fmt.Sprintf("usb%d", groupNumber)
				config.Ports[groupName] = mapKeysToSlice(currentGroup)
			}
			saveConfig(config)
			showMessage(win, "Profile saved.")
			time.Sleep(1 * time.Second)
			gc.FlushInput()
			return
		case 'e', 'E', 27:
			showMessage(win, "Exiting without saving current group.")
			time.Sleep(1 * time.Second)
			gc.FlushInput()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mapKeysToSlice(m map[string]bool) []string {
	s := make([]string, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	return s
}

// Auto Check Mode (без повторного тестирования).
// Каждые 100 мс опрашиваются USB-устройства. Для каждой выбранной группы ведётся учёт повторений:
// если устройство обнаружено и ранее не зафиксировано, progress увеличивается, затем state устанавливается в true;
// при отсутствии устройства state сбрасывается.
// Статус "[OK]" выставляется только когда progress >= требуемому числу тестов.
func autoCheckMode(win *gc.Window, config *Config, selectedGroups []string) {
	win.Timeout(100)
	var groups []string
	if len(config.Selected) > 0 {
		groups = config.Selected
	} else if len(selectedGroups) > 0 {
		groups = selectedGroups
	} else {
		for group := range config.Ports {
			groups = append(groups, group)
		}
	}
	if len(groups) == 0 {
		showMessage(win, "No groups available for checking.")
		time.Sleep(1 * time.Second)
		return
	}

	progress := make(map[string]int)
	state := make(map[string]bool)
	for _, group := range groups {
		progress[group] = 0
		state[group] = false
	}

	for {
		win.Erase()
		win.MovePrint(0, 2, "Auto Check Mode - Press Q or ESC to exit")
		current := getCurrentPortIDs()
		line := 2
		allVerified := true
		for _, group := range groups {
			ids := config.Ports[group]
			found := false
			for _, id := range ids {
				if current[id] {
					found = true
					break
				}
			}
			if found && !state[group] {
				progress[group]++
				state[group] = true
			} else if !found && state[group] {
				state[group] = false
			}
			req := config.TestCounts[group]
			if req <= 0 {
				req = 1
			}
			status := "[NO]"
			if progress[group] >= req {
				status = "[OK]"
			} else {
				allVerified = false
			}
			win.MovePrint(line, 2, fmt.Sprintf("%s [%d/%d] %s: %s", status, progress[group], req, group, strings.Join(ids, ", ")))
			line++
		}
		win.Refresh()
		if allVerified {
			showMessage(win, "All groups verified successfully.")
			time.Sleep(1 * time.Second)
			return
		}
		ch := win.GetChar()
		if ch == 'q' || ch == 'Q' || ch == 27 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	showMessage(win, "Exiting check mode.")
	time.Sleep(1 * time.Second)
}

// Retest Mode (Auto Test Mode).
// Для каждой выбранной группы используется статичное требуемое число тестов (например, 2).
// Логика для каждой группы:
// - State 0: ожидание вставки USB-носителя. При обнаружении увеличивается progress и state переключается в 1.
// - State 1: ожидание удаления USB-носителя. При отсутствии переключается в 0.
// Группа считается протестированной, если progress >= требуемому числу тестов.
func retestMode(win *gc.Window, config *Config, selectedGroups []string) {
	win.Timeout(100)
	var groups []string
	if len(config.Selected) > 0 {
		groups = config.Selected
	} else if len(selectedGroups) > 0 {
		groups = selectedGroups
	} else {
		for group := range config.Ports {
			groups = append(groups, group)
		}
	}
	if len(groups) == 0 {
		showMessage(win, "No groups available for testing.")
		time.Sleep(1 * time.Second)
		return
	}

	required := make(map[string]int)
	progress := make(map[string]int)
	state := make(map[string]int) // 0 - ожидание вставки, 1 - ожидание удаления
	for _, group := range groups {
		req := config.TestCounts[group]
		if req <= 0 {
			req = 1
		}
		required[group] = req
		progress[group] = 0
		state[group] = 0
	}

	for {
		win.Erase()
		win.MovePrint(0, 2, "Auto Test Mode (Retest) - Press Q or ESC to exit")
		current := getCurrentPortIDs()
		line := 2
		allFinished := true
		for _, group := range groups {
			req := required[group]
			prog := progress[group]
			stat := "[NO]"
			if prog >= req {
				stat = "[OK]"
			} else {
				allFinished = false
			}
			win.MovePrint(line, 2, fmt.Sprintf("%s [%d/%d] %s: %s", stat, prog, req, group, strings.Join(config.Ports[group], ", ")))
			line++
		}
		win.Refresh()
		if allFinished {
			showMessage(win, "All groups tested successfully.")
			time.Sleep(1 * time.Second)
			return
		}
		ch := win.GetChar()
		if ch == 'q' || ch == 'Q' || ch == 27 {
			break
		}
		for _, group := range groups {
			if progress[group] >= required[group] {
				continue
			}
			ports := config.Ports[group]
			found := false
			for _, id := range ports {
				if current[id] {
					found = true
					break
				}
			}
			if state[group] == 0 && found {
				progress[group]++
				state[group] = 1
			} else if state[group] == 1 && !found {
				state[group] = 0
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	showMessage(win, "Exiting test mode.")
	time.Sleep(1 * time.Second)
}

// getDeviceNodeForPort возвращает первый device node, соответствующий USB-порту.
func getDeviceNodeForPort(portID string) string {
	devices := detectUSBDevices()
	for _, dev := range devices {
		if getPortID(dev) == portID {
			return dev
		}
	}
	return ""
}

// getMountPoint ищет точку монтирования для device node.
func getMountPoint(dev string) string {
	file, err := os.Open("/proc/mounts")
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == dev {
			return fields[1]
		}
	}
	return ""
}

// getSelectedGroups возвращает выбранные группы и их testCounts.
func getSelectedGroups(win *gc.Window, config *Config) ([]string, map[string]int) {
	return config.Selected, config.TestCounts
}

// selectPriorityGroups позволяет выбрать группы для тестирования с назначением testCount.
func selectPriorityGroups(win *gc.Window, config *Config) ([]string, map[string]int) {
	groups := []string{}
	for group := range config.Ports {
		groups = append(groups, group)
	}
	if len(groups) == 0 {
		return nil, nil
	}

	selected := make(map[int]bool)
	testCounts := make(map[int]int)
	current := 0

	for {
		win.Erase()
		win.MovePrint(1, 2, "Select groups for testing and set test count:")
		win.MovePrint(2, 2, "TAB - toggle, Enter - set count, ESC - finish")
		for i, group := range groups {
			marker := "[ ]"
			countStr := ""
			if selected[i] {
				marker = "[X]"
				if tc, ok := testCounts[i]; ok {
					countStr = fmt.Sprintf(" - %d tests", tc)
				}
			}
			line := 4 + i
			if i == current {
				win.AttrOn(gc.A_REVERSE)
			}
			win.MovePrint(line, 4, fmt.Sprintf("%s %s%s", marker, group, countStr))
			if i == current {
				win.AttrOff(gc.A_REVERSE)
			}
		}
		win.Refresh()
		ch := win.GetChar()
		if ch == gc.KEY_UP {
			if current > 0 {
				current--
			}
		} else if ch == gc.KEY_DOWN {
			if current < len(groups)-1 {
				current++
			}
		} else if ch == '\t' {
			if selected[current] {
				delete(selected, current)
				delete(testCounts, current)
			} else {
				selected[current] = true
				testCounts[current] = 1
			}
		} else if ch == 10 || ch == 13 {
			if selected[current] {
				win.Erase()
				prompt := fmt.Sprintf("Enter number of tests for group %s: ", groups[current])
				win.MovePrint(2, 2, prompt)
				win.Refresh()
				input := readLine(win, 3, 2)
				n, err := strconv.Atoi(strings.TrimSpace(input))
				if err == nil && n > 0 {
					testCounts[current] = n
				}
			} else {
				selected[current] = true
				testCounts[current] = 1
			}
		} else if ch == 27 {
			break
		}
	}

	selGroups := []string{}
	groupTestCounts := make(map[string]int)
	for i, sel := range selected {
		if sel {
			selGroups = append(selGroups, groups[i])
			groupTestCounts[groups[i]] = testCounts[i]
		}
	}
	return selGroups, groupTestCounts
}

// selectFromList отображает список для выбора.
func selectFromList(win *gc.Window, title string, items []string) int {
	current := 0
	for {
		win.Erase()
		win.MovePrint(1, 2, title)
		for i, item := range items {
			if i == current {
				win.AttrOn(gc.A_REVERSE)
			}
			win.MovePrint(3+i, 4, item)
			if i == current {
				win.AttrOff(gc.A_REVERSE)
			}
		}
		win.MovePrint(len(items)+5, 2, "Use arrow keys, Enter to select, ESC to cancel.")
		win.Refresh()
		ch := win.GetChar()
		if ch == gc.KEY_UP {
			if current > 0 {
				current--
			}
		} else if ch == gc.KEY_DOWN {
			if current < len(items)-1 {
				current++
			}
		} else if ch == 10 || ch == 13 {
			return current
		} else if ch == 27 {
			return -1
		}
	}
}

// editMode: редактирование группы.
func editMode(win *gc.Window, config *Config) {
	win.Erase()
	win.MovePrint(1, 2, "Edit USB Groups")
	if len(config.Ports) == 0 {
		win.MovePrint(3, 2, "No saved USB port groups. Press any key to return.")
		win.Refresh()
		win.GetChar()
		return
	}
	groups := []string{}
	for group, portIDs := range config.Ports {
		groups = append(groups, fmt.Sprintf("%s: %s", group, strings.Join(portIDs, ", ")))
	}
	choice := selectFromList(win, "Select a group to edit:", groups)
	if choice < 0 {
		return
	}
	selectedGroup := strings.Split(groups[choice], ":")[0]
	entries := config.Ports[selectedGroup]
	if len(entries) == 0 {
		showMessage(win, "This group is empty. Press any key to return.")
		win.GetChar()
		return
	}
	entryChoice := selectFromList(win, fmt.Sprintf("Select an entry to edit from group %s:", selectedGroup), entries)
	if entryChoice < 0 {
		return
	}
	win.Erase()
	win.MovePrint(1, 2, fmt.Sprintf("Editing entry %d in group %s", entryChoice+1, selectedGroup))
	win.MovePrint(3, 2, "Current value: "+entries[entryChoice])
	win.MovePrint(5, 2, "Enter new value (leave empty to cancel): ")
	win.Refresh()
	newValue := readLine(win, 6, 2)
	if strings.TrimSpace(newValue) != "" {
		entries[entryChoice] = strings.TrimSpace(newValue)
		config.Ports[selectedGroup] = entries
		showMessage(win, "Entry updated. Press any key to return.")
	} else {
		showMessage(win, "No changes made. Press any key to return.")
	}
	win.GetChar()
}

// deleteMode: удаление записи.
func deleteMode(win *gc.Window, config *Config) {
	win.Erase()
	win.MovePrint(1, 2, "Delete USB from Groups")
	if len(config.Ports) == 0 {
		win.MovePrint(3, 2, "No saved USB port groups. Press any key to return.")
		win.Refresh()
		win.GetChar()
		return
	}
	groups := []string{}
	for group, portIDs := range config.Ports {
		groups = append(groups, fmt.Sprintf("%s: %s", group, strings.Join(portIDs, ", ")))
	}
	choice := selectFromList(win, "Select a group for deletion:", groups)
	if choice < 0 {
		return
	}
	selectedGroup := strings.Split(groups[choice], ":")[0]
	entries := config.Ports[selectedGroup]
	if len(entries) == 0 {
		showMessage(win, "This group is empty. Press any key to return.")
		win.GetChar()
		return
	}
	entryChoice := selectFromList(win, fmt.Sprintf("Select an entry to delete from group %s:", selectedGroup), entries)
	if entryChoice < 0 {
		return
	}
	entries = append(entries[:entryChoice], entries[entryChoice+1:]...)
	if len(entries) == 0 {
		delete(config.Ports, selectedGroup)
	} else {
		config.Ports[selectedGroup] = entries
	}
	showMessage(win, "Entry deleted. Press any key to return.")
	win.GetChar()
}

// showMessage выводит сообщение и ждёт 1 секунду.
func showMessage(win *gc.Window, msg string) {
	win.Erase()
	win.MovePrint(2, 2, msg)
	win.Refresh()
	time.Sleep(1 * time.Second)
}

// readLine считывает строку с ввода (ESC для выхода).
func readLine(win *gc.Window, y, x int) string {
	var result []rune
	win.Move(y, x)
	win.Refresh()
	for {
		ch := win.GetChar()
		if ch == -1 {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if ch == 27 { // ESC
			break
		}
		if ch == '\n' || ch == '\r' {
			break
		}
		if ch == 127 || ch == gc.KEY_BACKSPACE {
			if len(result) > 0 {
				result = result[:len(result)-1]
				win.MovePrint(y, x, strings.Repeat(" ", 50))
				win.Move(y, x)
				win.Print(string(result))
				win.Refresh()
			}
			continue
		}
		result = append(result, rune(ch))
		win.Print(string(ch))
		win.Refresh()
	}
	return string(result)
}

// getMotherboardID возвращает идентификатор материнской платы.
func getMotherboardID() string {
	cmd := exec.Command("dmidecode", "-s", "baseboard-product-name")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "Unknown"
	}
	return strings.TrimSpace(string(output))
}

// detectUSBDevices возвращает список USB-устройств (например, "/dev/sdb") по lsblk.
func detectUSBDevices() []string {
	cmd := exec.Command("lsblk", "-o", "NAME,TRAN", "--json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	var result struct {
		Blockdevices []struct {
			Name string `json:"name"`
			Tran string `json:"tran"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil
	}
	var devices []string
	for _, dev := range result.Blockdevices {
		if strings.ToLower(dev.Tran) == "usb" {
			devices = append(devices, "/dev/"+dev.Name)
		}
	}
	return devices
}

// getPortID возвращает идентификатор USB-порта для устройства.
func getPortID(device string) string {
	id := getPortIDFromSysfs(device)
	if id == "" {
		id = getPortIDFromUdev(device)
	}
	return id
}

func getPortIDFromSysfs(device string) string {
	baseDevice := filepath.Base(device)
	sysDevicePath := filepath.Join("/sys/class/block", baseDevice, "device")
	realPath, err := filepath.EvalSymlinks(sysDevicePath)
	if err != nil {
		return ""
	}
	for path := realPath; path != "/"; path = filepath.Dir(path) {
		base := filepath.Base(path)
		usbDevicePath := filepath.Join("/sys/bus/usb/devices", base)
		if info, err := os.Stat(usbDevicePath); err == nil && info.IsDir() {
			return base
		}
	}
	return ""
}

func getPortIDFromUdev(device string) string {
	cmd := exec.Command("udevadm", "info", "--query=property", "--name="+device)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "ID_PATH=") {
			return strings.TrimPrefix(line, "ID_PATH=")
		}
	}
	return ""
}

func getCurrentPortIDs() map[string]bool {
	portIDs := make(map[string]bool)
	for _, device := range detectUSBDevices() {
		if portID := getPortID(device); portID != "" {
			portIDs[portID] = true
		}
	}
	return portIDs
}

func loadConfig() *Config {
	config := &Config{
		Ports:      make(map[string][]string),
		Selected:   []string{},
		TestCounts: make(map[string]int),
	}
	data, err := ioutil.ReadFile(*configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return config
		}
		log.Fatal(err)
	}
	if err := json.Unmarshal(data, config); err != nil {
		log.Fatalf("Config parse error: %v", err)
	}
	if config.TestCounts == nil {
		config.TestCounts = make(map[string]int)
	}
	return config
}

func saveConfig(config *Config) {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := ioutil.WriteFile(*configFile, data, 0644); err != nil {
		log.Fatal(err)
	}
}
