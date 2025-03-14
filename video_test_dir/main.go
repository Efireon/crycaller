package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Константы и типы
const configFile = "./video_cfg.json"
const drmPath = "/sys/class/drm/"

var (
	red   = "\033[0;31m"
	green = "\033[0;32m"
	nc    = "\033[0m"
)

// VideoPort соответствует записи в конфигурационном JSON
type VideoPort struct {
	Name string `json:"name"`
	Test bool   `json:"test"`
}

// Config соответствует общей конфигурации
type Config struct {
	VideoPorts []VideoPort `json:"video_ports"`
}

// listPorts возвращает список видео портов из /sys/class/drm,
// отфильтрованных по именам: HDMI, VGA, DP, eDP, DVI, USB-C, Thunderbolt.
func listPorts() ([]string, error) {
	// Проверяем, что директория существует
	info, err := os.Stat(drmPath)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%sPath %s does not exist. Ensure that DRM is supported on your system.%s", red, drmPath, nc)
	}

	// Используем glob для поиска путей вида /sys/class/drm/card*/*
	pattern := filepath.Join(drmPath, "card*/*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}

	// Регулярное выражение для проверки нужных имен коннекторов
	// После удаления префикса вида "cardX-"
	re := regexp.MustCompile(`^(HDMI|VGA|DP|eDP|DVI|USB-C|Thunderbolt)`)
	portsMap := make(map[string]bool)
	for _, fullPath := range matches {
		// Берём только имя файла/директории
		base := filepath.Base(fullPath)
		// Удаляем префикс "card[0-9]*-"
		reCard := regexp.MustCompile(`^card[0-9]+-`)
		connectorName := reCard.ReplaceAllString(base, "")

		if re.MatchString(connectorName) {
			portsMap[base] = true
		}
	}

	// Из карты делаем слайс и сортируем (удаляем дубликаты)
	var ports []string
	for p := range portsMap {
		ports = append(ports, p)
	}
	sort.Slice(ports, func(i, j int) bool {
		return ports[i] < ports[j]
	})
	return ports, nil
}

// readStatus возвращает содержимое файла /sys/class/drm/<port>/status
func readStatus(port string) (string, error) {
	statusFile := filepath.Join(drmPath, port, "status")
	data, err := ioutil.ReadFile(statusFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// setPorts обрабатывает режимы создания конфигурации:
// mode == "ALL"  – сохранить все порты (test=false),
// mode == "CON"  – сохранить только подключенные (test=true),
// mode == "test" – выбрать порты для теста (test=true),
// mode == "work" – выбрать порты для работы (test=false).
func setPorts(mode string) {
	ports, err := listPorts()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if mode == "ALL" {
		if len(ports) == 0 {
			fmt.Printf("%sNo video ports found.%s\n", red, nc)
			os.Exit(1)
		}
		var cfg Config
		for _, port := range ports {
			cfg.VideoPorts = append(cfg.VideoPorts, VideoPort{Name: port, Test: false})
		}
		writeConfig(cfg)
		fmt.Printf("%sConfiguration saved to %s%s\n", green, configFile, nc)
		return
	}

	if mode == "CON" {
		var connectedPorts []string
		for _, port := range ports {
			status, err := readStatus(port)
			if err == nil && status == "connected" {
				connectedPorts = append(connectedPorts, port)
			}
		}
		if len(connectedPorts) == 0 {
			fmt.Printf("%sNo connected video ports found.%s\n", red, nc)
			os.Exit(1)
		}
		var cfg Config
		for _, port := range connectedPorts {
			cfg.VideoPorts = append(cfg.VideoPorts, VideoPort{Name: port, Test: true})
		}
		writeConfig(cfg)
		fmt.Printf("%sConfiguration saved to %s%s\n", green, configFile, nc)
		return
	}

	// Режимы "test" и "work": выводим список портов и просим пользователя выбрать номера.
	if len(ports) == 0 {
		fmt.Printf("%sNo video ports found.%s\n", red, nc)
		os.Exit(1)
	}

	fmt.Println("Available video ports:")
	// Для отображения убираем префикс "card[0-9]+-"
	reCard := regexp.MustCompile(`^card[0-9]+-`)
	for i, port := range ports {
		displayName := reCard.ReplaceAllString(port, "")
		fmt.Printf("%d. %s\n", i+1, displayName)
	}

	fmt.Print("Select ports for testing (enter numbers separated by space): ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("%sError reading input: %v%s\n", red, err, nc)
		os.Exit(1)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		fmt.Printf("%sNo ports selected for testing.%s\n", red, nc)
		os.Exit(1)
	}

	parts := strings.Fields(line)
	var selected []string
	for _, part := range parts {
		num, err := strconv.Atoi(part)
		if err != nil || num < 1 || num > len(ports) {
			fmt.Printf("%sInvalid port number: %s%s\n", red, part, nc)
			os.Exit(1)
		}
		selected = append(selected, ports[num-1])
	}

	var cfg Config
	testFlag := true
	if mode == "work" {
		testFlag = false
	}
	for _, port := range selected {
		cfg.VideoPorts = append(cfg.VideoPorts, VideoPort{Name: port, Test: testFlag})
	}
	writeConfig(cfg)
	fmt.Printf("%sConfiguration saved to %s%s\n", green, configFile, nc)
}

// writeConfig сохраняет конфигурацию в JSON-файл.
func writeConfig(cfg Config) {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Printf("%sError marshaling config: %v%s\n", red, err, nc)
		os.Exit(1)
	}
	err = ioutil.WriteFile(configFile, data, 0644)
	if err != nil {
		fmt.Printf("%sError writing config file: %v%s\n", red, err, nc)
		os.Exit(1)
	}
}

// readConfig считывает конфигурацию из файла.
func readConfig() (Config, error) {
	var cfg Config
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

// readSingleChar читает один символ с терминала без ожидания Enter.
func readSingleChar() (rune, error) {
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return 0, err
	}
	defer term.Restore(fd, oldState)

	var buf [1]byte
	n, err := os.Stdin.Read(buf[:])
	if err != nil || n != 1 {
		return 0, fmt.Errorf("failed to read a character")
	}
	return rune(buf[0]), nil
}

// checkPorts выполняет проверку портов согласно конфигурационному файлу.
func checkPorts() {
	cfg, err := readConfig()
	if err != nil {
		fmt.Printf("%sConfiguration file %s not found. Use -s to create one.%s\n", red, configFile, nc)
		os.Exit(1)
	}

	if len(cfg.VideoPorts) == 0 {
		fmt.Printf("%sNo ports found in the configuration.%s\n", red, nc)
		os.Exit(1)
	}

	fmt.Println("Checking video ports from configuration:")

	// Получим актуальный список портов для проверки существования
	currentPorts, err := listPorts()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	portsMap := make(map[string]bool)
	for _, p := range currentPorts {
		portsMap[p] = true
	}

	reCard := regexp.MustCompile(`^card[0-9]+-`)
	for _, vp := range cfg.VideoPorts {
		displayPort := reCard.ReplaceAllString(vp.Name, "")
		if vp.Test {
			// Режим тестирования: проверяем статус подключения и спрашиваем подтверждение
			status, err := readStatus(vp.Name)
			if err != nil {
				fmt.Printf("%sCannot determine the status of port %s.%s\n", red, displayPort, nc)
				fmt.Printf("\nTest FAILED.\nFailed ports:\n- %s (status unknown)\n", displayPort)
				os.Exit(1)
			}
			if status != "connected" {
				fmt.Printf("%sERROR: Port %s is NOT connected.%s\n", red, displayPort, nc)
				fmt.Printf("\nTest FAILED.\nFailed ports:\n- %s (not connected)\n", displayPort)
				os.Exit(1)
			}

			fmt.Printf("Is there output on port %s? (y/n): ", displayPort)
			char, err := readSingleChar()
			fmt.Println() // переход на новую строку после ввода символа
			if err != nil {
				fmt.Printf("%sError reading input: %v%s\n", red, err, nc)
				os.Exit(1)
			}
			switch char {
			case 'y', 'Y':
				fmt.Printf("%sPort %s confirmed.%s\n", green, displayPort, nc)
			case 'n', 'N':
				fmt.Printf("%sPort %s NOT confirmed.%s\n", red, displayPort, nc)
				fmt.Printf("\nTest FAILED.\nFailed ports:\n- %s (not confirmed)\n", displayPort)
				os.Exit(1)
			default:
				fmt.Printf("%sInvalid input. Skipping port %s.%s\n", red, displayPort, nc)
				fmt.Printf("\nTest FAILED.\nFailed ports:\n- %s (invalid input)\n", displayPort)
				os.Exit(1)
			}
		} else {
			// Режим notest: проверяем, что порт существует в системе
			if _, exists := portsMap[vp.Name]; exists {
				fmt.Printf("%sPort %s exists in the system.%s\n", green, displayPort, nc)
			} else {
				fmt.Printf("%sERROR: Port %s does NOT exist in the system.%s\n", red, displayPort, nc)
				fmt.Printf("\nTest FAILED.\nFailed ports:\n- %s (does not exist)\n", displayPort)
				os.Exit(1)
			}
		}
	}
	fmt.Printf("\n%sAll ports passed the tests.%s\n", green, nc)
	os.Exit(0)
}

// countPorts выводит количество доступных видео портов.
func countPorts() {
	ports, err := listPorts()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	fmt.Printf("Number of video ports: %d\n", len(ports))
}

// showHelp выводит справку по использованию программы.
func showHelp() {
	helpText := `Usage: video_test [-s [work|ALL|CON]] [-c] [-h]
  -s [work|ALL|CON]    Set ports and save to config.
                       If 'work' is specified, ports are marked as 'notest'.
                       If 'ALL' is specified, all system video ports are saved without selection.
                       If 'CON' is specified, all connected video ports are saved without selection and marked as 'test: true'.
                       If -s is provided without argument, 'test' mode is assumed.
  -c                   Check ports based on the config.
  -h                   Display this help message.
`
	fmt.Println(helpText)
}

func main() {
	// Определяем флаги.
	// Для имитации опционального аргумента у -s установим значение по умолчанию пустую строку.
	sFlag := flag.String("s", "", "Set ports and save to config (optional mode: work, ALL, CON). If omitted, test mode is used.")
	cFlag := flag.Bool("c", false, "Check ports based on the config.")
	hFlag := flag.Bool("h", false, "Display help message.")

	flag.Parse()

	// Если нет флагов, вывести количество портов.
	if len(os.Args) == 1 {
		countPorts()
		return
	}

	// Если запрошена справка, выводим и выходим.
	if *hFlag {
		showHelp()
		return
	}

	// Если указан флаг -s, обрабатываем установку конфигурации.
	if flag.Lookup("s").Value.String() != "" || containsFlag("-s", os.Args) {
		// Если значение пустое, то mode = "test"
		mode := *sFlag
		if mode == "" {
			mode = "test"
		} else if mode != "work" && mode != "ALL" && mode != "CON" && mode != "test" {
			fmt.Printf("%sInvalid argument for -s: %s%s\n", red, mode, nc)
			showHelp()
			os.Exit(1)
		}
		setPorts(mode)
		return
	}

	// Если указан флаг -c, выполняем проверку портов.
	if *cFlag {
		checkPorts()
		return
	}

	// По умолчанию выводим количество портов.
	countPorts()
}

// containsFlag проверяет, присутствует ли флаг (например, "-s") в аргументах командной строки.
func containsFlag(flagName string, args []string) bool {
	for _, arg := range args {
		// Флаг может быть вида "-s" или "--s"
		if arg == flagName || arg == "--"+strings.TrimLeft(flagName, "-") {
			return true
		}
		// Возможны случаи, когда флаг и значение объединены, например, "-sALL"
		if strings.HasPrefix(arg, flagName) && len(arg) > len(flagName) {
			return true
		}
	}
	return false
}
