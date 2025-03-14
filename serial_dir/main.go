package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	serialFile = "SERIAL"
	efiCont    = "ctefi"
)

var (
	cDir        string // текущая рабочая директория
	mbSN        string // серийный номер материнской платы (ввод пользователя)
	ioSN        string // серийный номер IO (для продукта "Silver")
	mac         string // MAC-адрес (ввод пользователя)
	rtDrv       string // имя удалённого конфликтующего драйвера
	productName string // имя продукта из dmidecode (например, "Silver" или "IFMBH610MTPR")

	// Новые параметры для логирования
	logToFile bool   // флаг для сохранения лога в файл
	logServer string // адрес сервера для отправки лога (формат: user@host:path)
)

// ANSI escape sequences для цветного вывода
const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
)

// Section представляет секцию из вывода dmidecode
type Section struct {
	Handle     string                 `json:"handle,omitempty"`
	Title      string                 `json:"title,omitempty"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// LogData структура для хранения информации о процессе
type LogData struct {
	Timestamp       string                 `json:"timestamp"`
	ProductName     string                 `json:"product_name"`
	MbSerialNumber  string                 `json:"mb_serial_number"`
	IoSerialNumber  string                 `json:"io_serial_number,omitempty"`
	MacAddress      string                 `json:"mac_address"`
	OriginalSerial  string                 `json:"original_serial"`
	ActionPerformed string                 `json:"action_performed"`
	Success         bool                   `json:"success"`
	SystemInfo      map[string]interface{} `json:"system_info"`
}

func debugPrint(message string) {
	fmt.Println(colorCyan + "DEBUG: " + message + colorReset)
}

func main() {
	// Добавляем флаги для логирования
	logFilePtr := flag.Bool("log", true, "Save log to file")
	logServerPtr := flag.String("server", "", "Server to send log to (format: user@host:path)")
	flag.Parse()

	logToFile = *logFilePtr
	logServer = *logServerPtr

	// Требуются root-привилегии
	if os.Geteuid() != 0 {
		log.Fatal(colorRed + "[ERROR] Please run this program with root privileges." + colorReset)
	}

	var err error
	cDir, err = os.Getwd()
	if err != nil {
		log.Fatalf(colorRed+"[ERROR] Could not get current directory: %v"+colorReset, err)
	}

	fmt.Println(colorBlue + "Starting serial number modification..." + colorReset)

	// 1. Считываем серийники и MAC от пользователя
	if err := getSerialAndMac(); err != nil {
		log.Fatalf(colorRed+"[ERROR] %v"+colorReset, err)
	}

	debugPrint("User provided MB Serial: " + mbSN)
	if ioSN != "" {
		debugPrint("User provided IO Serial: " + ioSN)
	}
	debugPrint("User provided MAC: " + mac)

	// 2. Получаем системные серийники через dmidecode
	baseSerial, err := getSystemSerial("baseboard")
	if err != nil {
		log.Printf(colorYellow+"[WARNING] Could not get baseboard serial: %v"+colorReset, err)
	} else {
		debugPrint("System Baseboard Serial: " + baseSerial)
	}

	sysSerial, err := getSystemSerial("system")
	if err != nil {
		log.Printf(colorYellow+"[WARNING] Could not get system serial: %v"+colorReset, err)
	} else {
		debugPrint("System Serial: " + sysSerial)
	}

	// Определяем, требуется ли перепрошивка по серийному номеру.
	// Для продукта "Silver" сравниваются оба номера (mbSN и ioSN),
	// для "IFMBH610MTPR" только mbSN.
	needSerialFlash := false
	if productName == "Silver" {
		if mbSN != baseSerial || ioSN != sysSerial {
			needSerialFlash = true
		}
	} else if productName == "IFMBH610MTPR" {
		if mbSN != baseSerial {
			needSerialFlash = true
		}
	}

	// Определяем, совпадает ли вводимый MAC с тем, что уже присутствует.
	targetMAC := strings.ToLower(mac)
	macAlreadySet := false
	if ifaces, err := getInterfacesWithMAC(targetMAC); err == nil && len(ifaces) > 0 {
		macAlreadySet = true
	}

	// Переменная для записи выполненных действий
	actionPerformed := ""
	success := true

	reader := bufio.NewReader(os.Stdin)
	// Если ни перепрошивка по серийному номеру не требуется, ни MAC не нужно менять:
	if !needSerialFlash && macAlreadySet {
		actionPerformed = "No changes required"
		fmt.Println(colorGreen + "No reflash required – system already has the correct serial number and MAC address." + colorReset)

		// Создаем лог перед завершением
		createOperationLog(actionPerformed, success, baseSerial)

		fmt.Print("Poweroff system now? (Y/n): ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if !strings.EqualFold(choice, "n") {
			fmt.Println("Powering off system...")
			_ = runCommandNoOutput("poweroff")
		} else {
			fmt.Println("Exiting without powering off. Please shutdown manually.")
		}
		os.Exit(0)
	} else if !needSerialFlash && !macAlreadySet {
		// Изменять нужно только MAC
		actionPerformed = "MAC address update only"
		fmt.Println(colorYellow + "Serial numbers match. Only MAC flash is required." + colorReset)
		if err := writeMac(mac); err != nil {
			success = false
			log.Fatalf(colorRed+"[ERROR] %v"+colorReset, err)
		}

		// Создаем лог перед завершением
		createOperationLog(actionPerformed, success, baseSerial)

		fmt.Print("MAC updated. Poweroff system now? (Y/n): ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if !strings.EqualFold(choice, "n") {
			fmt.Println("Powering off system...")
			_ = runCommandNoOutput("poweroff")
		} else {
			fmt.Println("Exiting without powering off. Please shutdown manually.")
		}
		os.Exit(0)
	} else {
		// Если требуется перепрошивка по серийному номеру (с возможной перепрошивкой MAC)
		if needSerialFlash && !macAlreadySet {
			actionPerformed = "Serial number and MAC address update"
		} else {
			actionPerformed = "Serial number update only"
		}

		// Сначала выполняем перепрошивку MAC (если оно ещё не установлено, writeMac само проверит это)
		if err := writeMac(mac); err != nil {
			success = false
			log.Fatalf(colorRed+"[ERROR] %v"+colorReset, err)
		}

		// Затем записываем серийный номер в файл
		if err := writeSerial(mbSN); err != nil {
			success = false
			log.Fatalf(colorRed+"[ERROR] %v"+colorReset, err)
		}

		// И вызываем функцию bootctl для установки одноразовой загрузочной записи и перепрошивки EFI.
		if err := bootctl(); err != nil {
			success = false
			log.Fatalf(colorRed+"[ERROR] bootctl error: %v"+colorReset, err)
		}

		// Создаем лог перед перезагрузкой
		createOperationLog(actionPerformed, success, baseSerial)

		// Запрашиваем перезагрузку системы
		fmt.Print("Serial number has been set. Reboot now? (Y/n): ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if strings.EqualFold(choice, "n") {
			fmt.Println("Please reboot manually to apply changes.")
		} else {
			fmt.Println("Rebooting system...")
			_ = runCommandNoOutput("reboot")
		}
	}
}

// Функция создания и сохранения лога операций
func createOperationLog(action string, success bool, originalSerial string) {
	fmt.Println(colorBlue + "Creating operation log..." + colorReset)

	// Получаем полный вывод dmidecode
	dmidecodeOutput, err := runCommand("dmidecode")
	if err != nil {
		fmt.Printf(colorYellow+"[WARNING] Could not get dmidecode output for log: %v"+colorReset, err)
		dmidecodeOutput = "Error getting dmidecode output"
	}

	// Парсим вывод dmidecode
	sections, err := parseDmidecodeOutput(dmidecodeOutput)
	if err != nil {
		fmt.Printf(colorYellow+"[WARNING] Could not parse dmidecode output: %v"+colorReset, err)
	}

	// Преобразуем секции в карту для JSON
	systemInfo := make(map[string]interface{})
	for _, sec := range sections {
		key := sec.Title
		if key == "" {
			key = "Unknown"
		}

		sectionData := make(map[string]interface{})
		if sec.Handle != "" {
			sectionData["handle"] = sec.Handle
		}
		if len(sec.Properties) > 0 {
			sectionData["properties"] = sec.Properties
		}

		// Если такой ключ уже существует, преобразуем значение в массив
		if existing, exists := systemInfo[key]; exists {
			switch v := existing.(type) {
			case []interface{}:
				systemInfo[key] = append(v, sectionData)
			default:
				systemInfo[key] = []interface{}{v, sectionData}
			}
		} else {
			systemInfo[key] = sectionData
		}
	}

	// Создаем структуру данных лога
	timestamp := time.Now().Format("2006-01-02T15:04:05")
	logData := LogData{
		Timestamp:       timestamp,
		ProductName:     productName,
		MbSerialNumber:  mbSN,
		IoSerialNumber:  ioSN,
		MacAddress:      mac,
		OriginalSerial:  originalSerial,
		ActionPerformed: action,
		Success:         success,
		SystemInfo:      systemInfo,
	}

	// Преобразуем в JSON
	jsonData, err := json.MarshalIndent(logData, "", "  ")
	if err != nil {
		fmt.Printf(colorYellow+"[WARNING] Could not create JSON log: %v"+colorReset, err)
		return
	}

	// Генерируем имя файла для лога
	timeFormat := time.Now().Format("060102150405") // YYMMDDHHMMSS
	filename := fmt.Sprintf("%s_%s-%s.json", productName, mbSN, timeFormat)

	// Сохраняем лог в файл, если указан флаг
	if logToFile {
		logDir := filepath.Join(cDir, "logs")
		// Создаем директорию для логов, если она не существует
		if _, err := os.Stat(logDir); os.IsNotExist(err) {
			if err := os.Mkdir(logDir, 0755); err != nil {
				fmt.Printf(colorYellow+"[WARNING] Could not create log directory: %v"+colorReset, err)
				logDir = cDir
			}
		}

		logPath := filepath.Join(logDir, filename)
		if err := os.WriteFile(logPath, jsonData, 0644); err != nil {
			fmt.Printf(colorYellow+"[WARNING] Could not write log file: %v\n"+colorReset, err)
		} else {
			fmt.Printf(colorGreen+"[INFO] Log saved to: %s\n"+colorReset, logPath)
		}
	}

	// Если указан сервер для отправки лога, отправляем его
	if logServer != "" {
		// Создаем временный файл
		tempFile, err := os.CreateTemp("", "serial-log-*.json")
		if err != nil {
			fmt.Printf(colorYellow+"[WARNING] Could not create temporary file for log: %v"+colorReset, err)
			return
		}
		defer os.Remove(tempFile.Name())

		// Записываем JSON в файл
		if _, err := tempFile.Write(jsonData); err != nil {
			fmt.Printf(colorYellow+"[WARNING] Could not write to temporary file: %v"+colorReset, err)
			return
		}
		tempFile.Close()

		// Разбираем строку сервера на хост и путь
		var host, remotePath string
		parts := strings.SplitN(logServer, ":", 2)

		host = parts[0]
		if len(parts) > 1 {
			remotePath = parts[1]
		}

		// Создаем удаленную директорию перед отправкой файла
		if remotePath != "" {
			// Убираем слэш в конце пути, если есть
			remotePath = strings.TrimSuffix(remotePath, "/")

			// Создаем директорию на удаленном сервере
			mkdirCmd := exec.Command("ssh", host, "mkdir", "-p", remotePath)
			_, err := mkdirCmd.CombinedOutput()
			if err != nil {
				fmt.Printf(colorYellow+"[WARNING] Could not create remote directory: %v"+colorReset, err)
			}
		}

		// Строим правильный путь для SCP
		var destination string
		if remotePath != "" {
			destination = fmt.Sprintf("%s:%s/%s", host, remotePath, filename)
		} else {
			destination = fmt.Sprintf("%s:%s", host, filename)
		}

		// Отправляем файл на сервер с помощью SCP
		cmd := exec.Command("scp", tempFile.Name(), destination)
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf(colorYellow+"[WARNING] Could not send log to server: %v\nOutput: %s\n"+colorReset, err, output)
		} else {
			fmt.Printf(colorGreen+"[INFO] Log sent to server: %s\n"+colorReset, destination)
		}
	}
}

// parseDmidecodeOutput парсит вывод dmidecode и разбивает его на секции
func parseDmidecodeOutput(output string) ([]Section, error) {
	var sections []Section
	var currentSection *Section
	expectingTitle := false
	var currentPropKey string

	// Собираем строки заголовка (до первой строки, начинающейся с "Handle")
	headerLines := []string{}
	inHeader := true

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if inHeader {
			if strings.HasPrefix(line, "Handle") {
				// Если заголовок не пуст, добавляем его как секцию с заголовком "Header"
				if len(headerLines) > 0 {
					headerSection := Section{
						Title: "Header",
						Properties: map[string]interface{}{
							"Content": strings.Join(headerLines, "\n"),
						},
					}
					sections = append(sections, headerSection)
				}
				inHeader = false
				// Обрабатываем текущую строку как начало секции
			} else {
				headerLines = append(headerLines, line)
				continue
			}
		}

		// Начинаем новую секцию
		if strings.HasPrefix(line, "Handle") {
			if currentSection != nil {
				sections = append(sections, *currentSection)
			}
			currentSection = &Section{
				Handle:     strings.TrimPrefix(line, "Handle "),
				Properties: make(map[string]interface{}),
			}
			expectingTitle = true
			currentPropKey = ""
			continue
		}

		// Если ожидается заголовок, присваиваем текущую строку как заголовок секции
		if expectingTitle {
			currentSection.Title = trimmed
			expectingTitle = false
			continue
		}

		// Обрабатываем строки со свойствами
		if colonIndex := strings.Index(trimmed, ":"); colonIndex != -1 {
			key := strings.TrimSpace(trimmed[:colonIndex])
			value := strings.TrimSpace(trimmed[colonIndex+1:])
			if existing, ok := currentSection.Properties[key]; ok {
				// Если свойство уже существует, преобразуем его в массив
				switch v := existing.(type) {
				case []string:
					currentSection.Properties[key] = append(v, value)
				case string:
					currentSection.Properties[key] = []string{v, value}
				default:
					currentSection.Properties[key] = value
				}
			} else {
				currentSection.Properties[key] = value
			}
			currentPropKey = key
		} else {
			// Если строка не содержит двоеточия, предполагаем, что это продолжение предыдущего свойства
			if currentPropKey != "" {
				if existing, ok := currentSection.Properties[currentPropKey]; ok {
					if str, ok2 := existing.(string); ok2 {
						currentSection.Properties[currentPropKey] = str + " " + trimmed
					} else if arr, ok2 := existing.([]string); ok2 {
						if len(arr) > 0 {
							arr[len(arr)-1] = arr[len(arr)-1] + " " + trimmed
							currentSection.Properties[currentPropKey] = arr
						} else {
							currentSection.Properties[currentPropKey] = trimmed
						}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if currentSection != nil {
		sections = append(sections, *currentSection)
	}
	return sections, nil
}

// bootctl выполняет монтирование внешнего EFI-раздела, копирование содержимого каталога efishell (ctefi)
// и установку одноразовой загрузочной записи (через setOneTimeBoot). Не изменять эту функцию!
func bootctl() error {
	// Определяем загрузочное устройство
	bootDev, err := findBootDevice()
	if err != nil {
		return fmt.Errorf("Could not determine boot device: %v", err)
	}

	// Ищем внешний EFI-раздел
	targetDevice, targetEfi, err := findExternalEfiPartition(bootDev)
	if err != nil || targetDevice == "" || targetEfi == "" {
		return errors.New("No external EFI partition found")
	}

	// Монтируем EFI-раздел во временную директорию
	mountPoint, err := os.MkdirTemp("", "efi_mount")
	if err != nil {
		return fmt.Errorf("Could not create temporary mount point: %v", err)
	}
	defer os.RemoveAll(mountPoint)
	debugPrint("targetEFI: " + targetEfi)

	if err := runCommandNoOutput("mount", targetEfi, mountPoint); err != nil {
		return fmt.Errorf("Could not mount EFI partition: %v", err)
	}
	debugPrint("EFI partition mounted at: " + mountPoint)

	// Копируем содержимое каталога ctefi в корень смонтированного EFI-раздела
	cpCmd := fmt.Sprintf("cp -r %s/* %s", efiCont, mountPoint)
	if err := runCommandNoOutput("sh", "-c", cpCmd); err != nil {
		return fmt.Errorf("Failed to copy EFI content: %v", err)
	}
	debugPrint("Contents of " + efiCont + " copied to EFI partition.")

	// Вызываем функцию setOneTimeBoot для создания новой записи и установки BootNext
	if err := setOneTimeBoot(targetDevice, targetEfi); err != nil {
		_ = runCommandNoOutput("umount", mountPoint)
		return fmt.Errorf("setOneTimeBoot error: %v", err)
	}

	if err = runCommandNoOutput("bootctl", "set-oneshot", "03-efishell.conf"); err != nil {
		_ = runCommandNoOutput("umount", mountPoint)
		log.Fatalf(colorRed+"[ERROR] Failed to set one-time boot entry: %v"+colorReset, err)
	} else {
		debugPrint("One-time boot entry set successfully.")
	}

	// Отмонтируем EFI-раздел
	if err := runCommandNoOutput("umount", mountPoint); err != nil {
		return fmt.Errorf("Failed to unmount EFI partition: %v", err)
	}
	debugPrint("EFI partition unmounted.")

	return nil
}

func runCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func runCommandNoOutput(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	// Не выводим полный вывод, оставляем только отладочные сообщения
	var dummy bytes.Buffer
	cmd.Stdout = &dummy
	cmd.Stderr = &dummy
	return cmd.Run()
}

func findBootDevice() (string, error) {
	output, err := runCommand("findmnt", "/", "-o", "SOURCE", "-n")
	if err != nil {
		return "", fmt.Errorf("findmnt failed: %v", err)
	}
	output = strings.TrimSpace(output)
	loopRegex := regexp.MustCompile(`^/dev/loop[0-9]+$`)
	if output == "airootfs" || loopRegex.MatchString(output) {
		return "LOOP", nil
	}
	// Для NVMe-устройств имя выглядит как "/dev/nvme0n1p1" – родительский диск: "/dev/nvme0n1"
	if strings.Contains(output, "nvme") {
		devRegex := regexp.MustCompile(`p[0-9]+$`)
		return devRegex.ReplaceAllString(output, ""), nil
	}
	// Для остальных устройств, например "/dev/sda2" – родительский диск: "/dev/sda"
	devRegex := regexp.MustCompile(`[0-9]+$`)
	return devRegex.ReplaceAllString(output, ""), nil
}

func listRealDisks() ([]string, error) {
	output, err := runCommand("lsblk", "-d", "-o", "NAME,TYPE", "-rn")
	if err != nil {
		return nil, fmt.Errorf("lsblk failed: %v", err)
	}
	var disks []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "disk" {
			disks = append(disks, "/dev/"+fields[0])
		}
	}
	return disks, nil
}

func isEfiPartition(part string) bool {
	output, err := runCommand("blkid", "-o", "export", part)
	if err != nil {
		return false
	}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if matched, _ := regexp.MatchString(`^TYPE=(fat|vfat|msdos)`, line); matched {
			return true
		}
	}
	return false
}

func findExternalEfiPartition(bootDev string) (string, string, error) {
	disks, err := listRealDisks()
	if err != nil {
		return "", "", err
	}
	for _, dev := range disks {
		if dev == bootDev {
			continue
		}
		output, err := runCommand("lsblk", "-ln", "-o", "NAME", dev)
		if err != nil {
			continue
		}
		lines := strings.Split(output, "\n")
		for _, line := range lines {
			part := "/dev/" + strings.TrimSpace(line)
			if isEfiPartition(part) {
				return dev, part, nil
			}
		}
	}
	return "", "", nil
}

func getSerialAndMac() error {
	output, err := runCommand("dmidecode", "-t", "system")
	if err != nil {
		return fmt.Errorf("dmidecode failed: %v", err)
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Product Name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				productName = strings.TrimSpace(parts[1])
				break
			}
		}
	}
	if productName == "" {
		return errors.New("Could not determine Product Name. Make sure dmidecode is run with sufficient privileges.")
	}
	fmt.Printf("Product Name: %s\n", productName)

	requiredFields := map[string]*regexp.Regexp{}
	switch productName {
	case "Silver":
		requiredFields["mbSN"] = regexp.MustCompile(`^INF00A34[0-9]{7}$`)
		requiredFields["ioSN"] = regexp.MustCompile(`^INF00A44[0-9]{7}$`)
		requiredFields["mac"] = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	case "IFMBH610MTPR":
		requiredFields["mbSN"] = regexp.MustCompile(`^INF00A95[0-9]{7}$`)
		requiredFields["mac"] = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	default:
		return fmt.Errorf("Unknown product name: %s", productName)
	}

	fmt.Println("Please enter the following values (the program will automatically detect the type):")
	for key, regex := range requiredFields {
		fmt.Printf(" - %s (expected format: %s)\n", key, regex.String())
	}

	provided := make(map[string]string)
	reader := bufio.NewReader(os.Stdin)
	for len(provided) < len(requiredFields) {
		fmt.Print("Enter value: ")
		input, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		input = strings.TrimSpace(input)
		if input == "" {
			fmt.Println("Input cannot be empty. Please re-enter.")
			continue
		}
		matched := false
		for key, regex := range requiredFields {
			if _, ok := provided[key]; ok {
				continue
			}
			if regex.MatchString(input) {
				provided[key] = input
				fmt.Printf("%s value accepted: %s\n", key, input)
				matched = true
				break
			}
		}
		if !matched {
			fmt.Println("Input does not match any expected format. Please try again.")
		}
	}

	// Ожидаем ввод лишней (4-й) строки, но не более 500 мс.
	if _, err := readLineWithTimeout(500 * time.Millisecond); err != nil {
		debugPrint("No extra input received within 500ms, proceeding...")
	}

	if val, ok := provided["mbSN"]; ok {
		mbSN = val
	}
	if val, ok := provided["ioSN"]; ok {
		ioSN = val
	}
	if val, ok := provided["mac"]; ok {
		mac = val
	}

	fmt.Println("Collected data:")
	fmt.Printf("  mbSN: %s\n", mbSN)
	if productName == "Silver" {
		fmt.Printf("  ioSN: %s\n", ioSN)
	}
	fmt.Printf("  MAC: %s\n", mac)
	return nil
}

// readLineWithTimeout пытается считать строку из os.Stdin с заданным таймаутом.
// Для этого устанавливается неблокирующий режим на дескриптор и производится циклическая проверка.
func readLineWithTimeout(timeout time.Duration) (string, error) {
	fd := int(os.Stdin.Fd())
	// Устанавливаем неблокирующий режим.
	if err := syscall.SetNonblock(fd, true); err != nil {
		return "", err
	}
	// По окончании работы возвращаем обратно блокирующий режим.
	defer syscall.SetNonblock(fd, false)

	reader := bufio.NewReader(os.Stdin)
	deadline := time.Now().Add(timeout)
	for {
		// Если есть хотя бы один байт, читаем всю строку.
		_, err := reader.Peek(1)
		if err == nil {
			return reader.ReadString('\n')
		}
		// Если время вышло, прекращаем ожидание.
		if time.Now().After(deadline) {
			return "", errors.New("timeout reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func getSystemSerial(dmiType string) (string, error) {
	out, err := runCommand("dmidecode", "-t", dmiType)
	if err != nil {
		return "", err
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Serial Number:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "", errors.New("Serial Number not found")
}

func getInterfacesWithMAC(targetMAC string) ([]string, error) {
	output, err := runCommand("ip", "-o", "link", "show")
	if err != nil {
		return nil, fmt.Errorf("Failed to get ip link show: %v", err)
	}
	re := regexp.MustCompile(`^\d+:\s+([^:]+):.*link/ether\s+([0-9a-f:]+)`)
	var interfaces []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		matches := re.FindStringSubmatch(line)
		if len(matches) == 3 {
			iface := matches[1]
			macFound := matches[2]
			if strings.ToLower(macFound) == strings.ToLower(targetMAC) {
				interfaces = append(interfaces, iface)
			}
		}
	}
	if len(interfaces) == 0 {
		return nil, fmt.Errorf("No interface found with MAC address: %s", targetMAC)
	}
	return interfaces, nil
}

func writeMac(macInput string) error {
	targetMAC := strings.ToLower(macInput)
	// Если указанный MAC уже присутствует, пропускаем прошивку.
	if ifaces, err := getInterfacesWithMAC(targetMAC); err == nil && len(ifaces) > 0 {
		fmt.Printf(colorGreen+"[INFO] MAC address %s already present on interface(s): %s. Skipping flashing.\n"+colorReset,
			targetMAC, strings.Join(ifaces, ", "))
		return nil
	}

	out, err := runCommand("uname", "-m")
	if err != nil {
		return fmt.Errorf("Failed to get machine architecture: %v", err)
	}
	arch := strings.TrimSpace(out)
	rtnic := filepath.Join(cDir, "rtnicpg", "rtnicpg-"+arch)

	oldIface, oldIP, err := getActiveInterfaceAndIP()
	if err != nil {
		fmt.Printf(colorYellow+"[WARNING] %v"+colorReset, err)
	} else {
		debugPrint("Old IP address for interface " + oldIface + ": " + oldIP)
	}

	if err := loadDriver(); err != nil {
		return err
	}

	if err := os.Chmod(rtnic, 0755); err != nil {
		return fmt.Errorf("Failed to chmod %s: %v", rtnic, err)
	}

	modmac := strings.ReplaceAll(macInput, ":", "")
	fmt.Println(modmac)

	if err := runCommandNoOutput(rtnic, "/efuse", "/nodeid", modmac); err != nil {
		return fmt.Errorf("Failed to execute rtnic: %v", err)
	}
	fmt.Println("[INFO] MAC address was successfully written, verifying...")

	_ = runCommandNoOutput("rmmod", "pgdrv")
	if rtDrv != "" {
		if err := runCommandNoOutput("modprobe", rtDrv); err != nil {
			fmt.Printf(colorYellow+"[WARNING] Failed to modprobe %s: %v\n"+colorReset, rtDrv, err)
		}
	}

	ifaces, err := getInterfacesWithMAC(targetMAC)
	if err != nil {
		return fmt.Errorf("Failed to find interface with target MAC: %v", err)
	}
	debugPrint(fmt.Sprintf("Found interfaces with MAC %s: %v", targetMAC, ifaces))

	var newIface string
	if oldIface != "" {
		for _, iface := range ifaces {
			if iface == oldIface {
				newIface = iface
				break
			}
		}
	}
	if newIface == "" {
		newIface = ifaces[0]
		if len(ifaces) > 1 {
			fmt.Printf(colorYellow+"[WARNING] Multiple interfaces with matching MAC found. Using %s\n"+colorReset, newIface)
		}
	}

	if newIface != "" && oldIP != "" {
		maxRetries := 3
		var assignErr error
		for attempt := 1; attempt <= maxRetries; attempt++ {
			fmt.Printf("[INFO] Attempt %d: Restarting interface %s with IP %s\n", attempt, newIface, oldIP)
			_ = runCommandNoOutput("ip", "link", "set", "dev", newIface, "down")
			_ = runCommandNoOutput("ip", "addr", "flush", "dev", newIface)
			_ = runCommandNoOutput("ip", "link", "set", "dev", newIface, "address", targetMAC)
			_ = runCommandNoOutput("ip", "link", "set", "dev", newIface, "up")
			// Используем "replace" для назначения IP, чтобы заменить текущий адрес на старый.
			assignErr = runCommandNoOutput("ip", "addr", "replace", oldIP, "dev", newIface)
			if assignErr == nil {
				fmt.Printf("[INFO] Interface %s restarted with IP %s\n", newIface, oldIP)
				break
			} else {
				fmt.Printf(colorYellow+"[WARNING] Attempt %d: Failed to assign IP %s to interface %s: %v\n"+colorReset, attempt, oldIP, newIface, assignErr)
				if newIfaces, err := getInterfacesWithMAC(targetMAC); err == nil && len(newIfaces) > 0 {
					newIface = newIfaces[0]
					fmt.Printf("[INFO] Retrying with interface %s\n", newIface)
				} else {
					fmt.Println(colorYellow + "[WARNING] No interface with target MAC found on retry" + colorReset)
				}
			}
			if attempt == maxRetries && assignErr != nil {
				return fmt.Errorf("failed to assign IP after %d attempts: %v", maxRetries, assignErr)
			}
		}
	} else {
		fmt.Println(colorYellow + "[WARNING] Could not find interface for " + targetMAC + " or no previous IP was stored." + colorReset)
	}

	return nil
}

func getActiveInterfaceAndIP() (string, string, error) {
	output, err := runCommand("ip", "a")
	if err != nil {
		return "", "", fmt.Errorf("Failed to get 'ip a' output: %v", err)
	}

	lines := strings.Split(output, "\n")
	var currentIface, currentIP string
	headerRe := regexp.MustCompile(`^\d+:\s+([^:]+):\s+<([^>]+)>`)
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if matches := headerRe.FindStringSubmatch(line); len(matches) == 3 {
			ifaceName := matches[1]
			flags := matches[2]
			if ifaceName == "lo" {
				continue
			}
			if !strings.Contains(flags, "UP") {
				continue
			}
			currentIface = ifaceName
			for j := i + 1; j < len(lines); j++ {
				nextLine := strings.TrimSpace(lines[j])
				if nextLine == "" {
					continue
				}
				if headerRe.MatchString(nextLine) {
					break
				}
				if strings.HasPrefix(nextLine, "inet ") {
					fields := strings.Fields(nextLine)
					if len(fields) >= 2 {
						currentIP = fields[1]
						break
					}
				}
			}
			if currentIP != "" {
				break
			}
		}
	}

	if currentIface == "" {
		return "", "", errors.New("no active interface found")
	}
	if currentIP == "" {
		return currentIface, "", errors.New("active interface found but no IPv4 address detected")
	}
	return currentIface, currentIP, nil
}

func writeSerial(serial string) error {
	filePath := filepath.Join(cDir, efiCont, serialFile)
	fmt.Printf("[INFO] Writing %s...\n", filePath)
	return os.WriteFile(filePath, []byte(serial), 0644)
}

func loadDriver(args ...string) error {
	moduleDefault := "pgdrv"
	modulesToRemove := []string{"r8169", "r8168", "r8125", "r8101"}

	rtnicpgPath := filepath.Join(cDir, "rtnicpg")
	if info, err := os.Stat(rtnicpgPath); err != nil || !info.IsDir() {
		return fmt.Errorf("Directory %s does not exist", rtnicpgPath)
	}

	for _, mod := range modulesToRemove {
		if isModuleLoaded(mod) {
			fmt.Printf("Removing module: %s\n", mod)
			if err := runCommandNoOutput("rmmod", mod); err != nil {
				fmt.Printf("[WARNING] Could not remove module %s: %v\n", mod, err)
			} else {
				fmt.Printf("[INFO] Module %s successfully removed.\n", mod)
				rtDrv = mod
			}
		}
	}

	var targetModule string
	// Получаем версию ядра для включения её в имя файла драйвера.
	kernelVersion, err := runCommand("uname", "-r")
	if err != nil {
		kernelVersion = "unknown"
	} else {
		kernelVersion = strings.TrimSpace(kernelVersion)
	}
	if rtDrv != "" {
		targetModule = rtDrv + "_mod_" + kernelVersion + ".ko"
	} else {
		targetModule = moduleDefault + ".ko"
	}
	targetModulePath := filepath.Join(rtnicpgPath, targetModule)

	if _, err := os.Stat(targetModulePath); err == nil {
		fmt.Printf("[INFO] Found existing driver file %s. Loading it...\n", targetModulePath)
		modName := strings.TrimSuffix(targetModule, ".ko")
		if isModuleLoaded(modName) {
			fmt.Printf("[INFO] Module %s is already loaded.\n", modName)
			return nil
		}
		if err := runCommandNoOutput("insmod", targetModulePath); err != nil {
			return fmt.Errorf("Failed to load module %s: %v", targetModulePath, err)
		}
		fmt.Printf("[INFO] Module %s loaded successfully.\n", targetModule)
		return nil
	}

	fmt.Printf("[INFO] Compiling module %s.\n", moduleDefault)
	if err := runCommandNoOutput("make", "-C", rtnicpgPath, "clean", "all"); err != nil {
		return fmt.Errorf("Compilation failed: %v", err)
	}
	fmt.Println("[INFO] Compilation completed successfully.")

	builtModule := filepath.Join(rtnicpgPath, moduleDefault+".ko")
	if _, err := os.Stat(builtModule); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Compiled module %s not found", builtModule)
	}

	if rtDrv != "" {
		err := os.Rename(builtModule, targetModulePath)
		if err != nil {
			return fmt.Errorf("Failed to rename %s to %s: %v", builtModule, targetModulePath, err)
		}
	} else {
		targetModulePath = builtModule
	}

	if err := runCommandNoOutput("insmod", targetModulePath); err != nil {
		return fmt.Errorf("Failed to load module %s: %v", targetModulePath, err)
	}
	fmt.Printf("[INFO] Module %s loaded successfully.\n", targetModulePath)
	return nil
}

func isModuleLoaded(mod string) bool {
	out, err := runCommand("lsmod")
	if err != nil {
		return false
	}
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == mod {
			return true
		}
	}
	return false
}

func setOneTimeBoot(targetDevice, targetEfi string) error {
	// Используем регулярное выражение, которое не изменять – НЕ ТРОГАЕМ!
	re := regexp.MustCompile(`(?im)^Boot([0-9A-Fa-f]{4})(\*?)\s+OneTimeBoot\t(.+)$`)

	// Циклически удаляем все записи с лейблом "OneTimeBoot"
	for {
		// Получаем вывод efibootmgr (без вывода на консоль)
		out, err := runCommand("efibootmgr")
		if err != nil {
			return fmt.Errorf("efibootmgr failed: %v", err)
		}
		matches := re.FindAllStringSubmatch(out, -1)
		if len(matches) == 0 {
			// Записей с таким лейблом нет
			break
		}
		// Удаляем первую найденную запись
		bootNum := matches[0][1]
		debugPrint("[INFO] Removing existing OneTimeBoot entry: Boot" + bootNum)
		if err := runCommandNoOutput("efibootmgr", "-B", "-b", bootNum); err != nil {
			debugPrint(fmt.Sprintf("[WARNING] Failed to remove Boot%s: %v", bootNum, err))
		}
	}

	debugPrint("targetDevice: " + targetDevice)

	// Определяем номер раздела: например, если targetDevice="/dev/sda" и targetEfi="/dev/sda2", то partition будет "2"
	var partition string
	// Для NVMe-устройств имя выглядит как "/dev/nvme0n1p1" – родительский диск: "/dev/nvme0n1"
	if strings.Contains(targetDevice, "nvme") {
		partition = strings.TrimPrefix(targetEfi, targetDevice+"p")
	} else {
		partition = strings.TrimPrefix(targetEfi, targetDevice)
	}
	if partition == "" {
		return errors.New("could not determine partition number from targetEfi")
	}

	debugPrint("[INFO] Creating new OneTimeBoot entry")
	// Создаем новую запись, не выводя результат команды на консоль
	createCmd := exec.Command("efibootmgr",
		"-c",
		"-d", targetDevice,
		"-p", partition,
		"-L", "OneTimeBoot",
		"-l", "\\EFI\\BOOT\\bootx64.efi")
	// Убираем вывод efibootmgr, оставляем только отладочные сообщения
	var createOut bytes.Buffer
	createCmd.Stdout = &createOut
	createCmd.Stderr = &createOut
	if err := createCmd.Run(); err != nil {
		debugPrint("[ERROR] efibootmgr create output: " + createOut.String())
		return fmt.Errorf("failed to create new boot entry: %v", err)
	}

	// Ищем созданную запись с лейблом OneTimeBoot
	out, err := runCommand("efibootmgr", "-v")
	if err != nil {
		return fmt.Errorf("efibootmgr failed after creation: %v", err)
	}
	matches := re.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return errors.New("new OneTimeBoot entry not found after creation")
	}
	bootNum := matches[len(matches)-1][1]
	debugPrint("[INFO] New OneTimeBoot entry created: Boot" + bootNum)

	// Устанавливаем BootNext на созданную запись
	if err := runCommandNoOutput("efibootmgr", "-n", bootNum); err != nil {
		out2, err2 := runCommand("efibootmgr", "-v")
		if err2 == nil && strings.Contains(out2, "BootNext: "+bootNum) {
			debugPrint("BootNext is already set to Boot" + bootNum)
			return nil
		}
		return fmt.Errorf("failed to set BootNext to %s: %v", bootNum, err)
	}

	out3, err3 := runCommand("efibootmgr", "-v")
	if err3 == nil && strings.Contains(out3, "BootNext: "+bootNum) {
		debugPrint("BootNext is set to Boot" + bootNum)
		return nil
	}

	return fmt.Errorf("failed to verify BootNext setting for Boot%s", bootNum)
}
