package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
	maxRetries = 3 // Maximum number of retry attempts for critical operations
)

var (
	cDir        string // текущая рабочая директория
	mbSN        string // серийный номер материнской платы (ввод пользователя)
	ioSN        string // серийный номер IO (для продукта "Silver")
	mac         string // MAC-адрес (ввод пользователя)
	rtDrv       string // имя удалённого конфликтующего драйвера
	productName string // имя продукта из dmidecode (например, "Silver" или "IFMBH610MTPR")

	// EFI variable configuration
	guidPrefix string // префикс для GUID переменной UEFI
	efiVarGUID string // сгенерированный GUID

	// Новые параметры для efivar
	efiSNName  string // имя переменной UEFI для серийного номера
	efiMACName string // имя переменной UEFI для MAC адреса

	// Новые параметры для логирования
	logToFile bool   // флаг для сохранения лога в файл
	logServer string // адрес сервера для отправки лога (формат: user@host:path)
)

// ANSI escape sequences для цветного вывода
const (
	colorReset   = "\033[0m"
	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorCyan    = "\033[36m"
	colorBgRed   = "\033[41m"
	colorBgGreen = "\033[42m"
)

// Section represents a section from dmidecode output
type Section struct {
	Handle     string                 `json:"handle,omitempty"`
	Title      string                 `json:"title,omitempty"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// LogData structure for storing process information
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
	EfiSNVarName    string                 `json:"efi_sn_var_name,omitempty"`  // для SerialNumber
	EfiMACVarName   string                 `json:"efi_mac_var_name,omitempty"` // для MAC
	EfiVarGUID      string                 `json:"efi_var_guid,omitempty"`
}

func debugPrint(message string) {
	fmt.Println(colorCyan + "DEBUG: " + message + colorReset)
}

// Функция для вывода критических ошибок с яркими плашками
func criticalError(message string) {
	// Создаем рамку для большей заметности
	lineLength := len(message) + 6 // добавляем отступы
	if lineLength < 80 {
		lineLength = 80 // минимальная ширина плашки
	}

	border := strings.Repeat("!", lineLength)
	spaces := strings.Repeat(" ", (lineLength-len(message)-2)/2)

	fmt.Println("")
	fmt.Println(colorBgRed + colorReset)
	fmt.Println(colorBgRed + border + colorReset)
	fmt.Println(colorBgRed + "!!!" + spaces + message + spaces + "!!!" + colorReset)
	fmt.Println(colorBgRed + border + colorReset)
	fmt.Println(colorBgRed + colorReset)
	fmt.Println("")
}

// Функция для вывода информации об успешном завершении
func successMessage(message string) {
	// Создаем рамку для большей заметности
	lineLength := len(message) + 6 // добавляем отступы
	if lineLength < 60 {
		lineLength = 60 // минимальная ширина плашки
	}

	border := strings.Repeat("=", lineLength)
	spaces := strings.Repeat(" ", (lineLength-len(message)-2)/2)

	fmt.Println("")
	fmt.Println(colorBgGreen + colorReset)
	fmt.Println(colorBgGreen + border + colorReset)
	fmt.Println(colorBgGreen + "  " + spaces + message + spaces + "  " + colorReset)
	fmt.Println(colorBgGreen + border + colorReset)
	fmt.Println(colorBgGreen + colorReset)
	fmt.Println("")
}

func main() {
	// Add flags for logging and EFI variables
	logFilePtr := flag.Bool("log", true, "Save log to file")
	logServerPtr := flag.String("server", "", "Server to send log to (format: user@host:path)")
	guidPrefixPtr := flag.String("guid-prefix", "", "Optional 8-hex-digit prefix for the generated GUID")
	efiSNPtr := flag.String("efisn", "SerialNumber", "Name of the UEFI variable for Serial Number (default: SerialNumber)")
	efiMACPtr := flag.String("efimac", "HexMac", "Name of the UEFI variable for MAC Address (default: HexMac)")
	flag.Parse()

	logToFile = *logFilePtr
	logServer = *logServerPtr
	guidPrefix = *guidPrefixPtr
	efiSNName = *efiSNPtr
	efiMACName = *efiMACPtr

	// Root privileges are required
	if os.Geteuid() != 0 {
		criticalError("Please run this program with root privileges")
		os.Exit(1)
	}

	var err error
	cDir, err = os.Getwd()
	if err != nil {
		criticalError("Could not get current directory: " + err.Error())
		os.Exit(1)
	}

	fmt.Println(colorBlue + "Starting serial number modification..." + colorReset)

	// 1. Read serial numbers and MAC from the user
	if err := getSerialAndMac(); err != nil {
		criticalError("Failed to get serial and MAC: " + err.Error())
		os.Exit(1)
	}

	debugPrint("User provided MB Serial: " + mbSN)
	if ioSN != "" {
		debugPrint("User provided IO Serial: " + ioSN)
	}
	debugPrint("User provided MAC: " + mac)

	// 2. Get system serial numbers via dmidecode
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

	// Determine if serial number reflashing is required
	needSerialFlash := false

	// Тщательно проверяем логику сравнения серийных номеров
	// При этом используем ТОЛЬКО mbSN для сравнения, IO Serial не проверяется
	if mbSN != baseSerial {
		needSerialFlash = true
		debugPrint("Serial flashing is required: current SN does not match requested")
		debugPrint(fmt.Sprintf("Current baseboard SN: %s, requested mbSN: %s", baseSerial, mbSN))
	} else {
		debugPrint("Serial numbers match, no flashing required")
	}

	// ioSN не проверяется, он используется только для логов
	if productName == "Silver" {
		debugPrint(fmt.Sprintf("IO SN (%s) is only used for logging purposes, not for comparison", ioSN))
	} else if productName == "IFMBH610MTPR" {
		// Для IFMBH610MTPR продукта сравниваем только mbSN с baseSerial
		if mbSN != baseSerial {
			needSerialFlash = true
			debugPrint("Serial flashing is required: current SN does not match requested")
			debugPrint(fmt.Sprintf("Current baseboard SN: %s, requested: %s", baseSerial, mbSN))
		} else {
			debugPrint("Serial numbers match, no flashing required")
		}
	}

	// Determine if entered MAC matches what is already present
	targetMAC := strings.ToLower(mac)
	macAlreadySet := false
	if ifaces, err := getInterfacesWithMAC(targetMAC); err == nil && len(ifaces) > 0 {
		macAlreadySet = true
		debugPrint(fmt.Sprintf("MAC %s is already present on interfaces: %s", targetMAC, strings.Join(ifaces, ", ")))
	} else {
		debugPrint(fmt.Sprintf("MAC %s not found in system, flashing is required", targetMAC))
	}

	// Variable to record performed actions
	actionPerformed := ""
	success := true

	reader := bufio.NewReader(os.Stdin)

	// Handle different scenarios based on what needs updating
	if !needSerialFlash && macAlreadySet {
		// CASE 1: Both serial numbers and MAC already match - no changes needed
		actionPerformed = "No changes required"
		successMessage("No reflash required – system already has the correct serial number and MAC address")

		// Create log before completion
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
	} else if !needSerialFlash && !macAlreadySet {
		// CASE 2: Serial numbers match but MAC needs updating
		actionPerformed = "MAC address update only"
		fmt.Println(colorYellow + "Serial numbers match. Only MAC flash is required." + colorReset)

		// Clear any existing MAC EFI variables
		if err := clearEfiVariables(efiMACName); err != nil {
			fmt.Printf(colorYellow+"[WARNING] Failed to clear extra EFI variables for %s: %v\n"+colorReset, efiMACName, err)
		} else {
			debugPrint("Cleared extra EFI variables for " + efiMACName)
		}

		// Пытаемся обновить MAC через драйвер с повторными попытками
		if err := writeMAcWithRetries(mac); err != nil {
			success = false
			criticalError("MAC address could not be written after multiple attempts. It is recommended to power off the system and diagnose the hardware manually.")
		} else {
			// Generate GUID for EFI variables if not already generated
			if efiVarGUID == "" {
				efiVarGUID, err = randomGUIDWithPrefix(guidPrefix)
				if err != nil {
					fmt.Printf(colorYellow+"[WARNING] Failed to generate GUID: %v\n"+colorReset, err)
				} else {
					debugPrint("Generated EFI variable GUID: " + efiVarGUID)
				}
			}

			// После успешного обновления MAC, записываем его также в EFI-переменную
			if efiVarGUID != "" {
				if err := writeMACToEfiVar(mac); err != nil {
					fmt.Printf(colorYellow+"[WARNING] Failed to write MAC to EFI variable: %v\n"+colorReset, err)
				}
			}
		}

		// Создаём лог
		createOperationLog(actionPerformed, success, baseSerial)

		if success {
			successMessage("MAC address updated successfully")
		}

		fmt.Print("Poweroff system now? (Y/n): ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if !strings.EqualFold(choice, "n") {
			fmt.Println("Powering off system...")
			_ = runCommandNoOutput("poweroff")
		} else {
			fmt.Println("Exiting without powering off. Please shutdown manually.")
		}
	} else if needSerialFlash {
		// CASE 3: Serial numbers need updating (MAC may or may not need updating)
		// Определяем, какие действия будут выполняться:
		if !macAlreadySet {
			actionPerformed = "Serial number and MAC address update"
		} else {
			actionPerformed = "Serial number update only"
		}

		// First, flash MAC if it's not already set
		if !macAlreadySet {
			if err := writeMAcWithRetries(mac); err != nil {
				success = false
				criticalError("MAC address could not be written after multiple attempts. It is recommended to power off the system and diagnose the hardware manually.")

				// Create log before exiting
				createOperationLog("MAC address update failed", false, baseSerial)

				fmt.Print("Poweroff system now? (Y/n): ")
				choice, _ := reader.ReadString('\n')
				choice = strings.TrimSpace(choice)
				if !strings.EqualFold(choice, "n") {
					fmt.Println("Powering off system...")
					_ = runCommandNoOutput("poweroff")
				} else {
					fmt.Println("Exiting without powering off. Please shutdown manually.")
				}
				os.Exit(1)
			}
		} else {
			fmt.Println(colorGreen + "[INFO] MAC address already set correctly, skipping MAC update." + colorReset)
		}

		// Clear existing EFI variables for both Serial Number and MAC
		if err := clearEfiVariables(efiSNName); err != nil {
			fmt.Printf(colorYellow+"[WARNING] Failed to clear extra EFI variables for %s: %v\n"+colorReset, efiSNName, err)
		} else {
			debugPrint("Cleared extra EFI variables for " + efiSNName)
		}

		if err := clearEfiVariables(efiMACName); err != nil {
			fmt.Printf(colorYellow+"[WARNING] Failed to clear extra EFI variables for %s: %v\n"+colorReset, efiMACName, err)
		} else {
			debugPrint("Cleared extra EFI variables for " + efiMACName)
		}

		// Now update serial number
		// Generate GUID for EFI variable
		efiVarGUID, err = randomGUIDWithPrefix(guidPrefix)
		if err != nil {
			success = false
			criticalError("Failed to generate GUID: " + err.Error())
			os.Exit(1)
		}
		debugPrint("Generated EFI variable GUID: " + efiVarGUID)

		// Write serial number to EFI variable with retries
		var serialWriteSuccess bool = false

		// В EFI-переменную и в файл SERIAL всегда записываем только mbSN
		// ioSN используется только для логов

		for retry := 0; retry < maxRetries; retry++ {
			if err := writeSerialToEfiVar(mbSN); err != nil {
				fmt.Printf(colorYellow+"[WARNING] Attempt %d: Failed to write serial to EFI variable: %v"+colorReset+"\n", retry+1, err)
				if retry == maxRetries-1 {
					criticalError("Failed to write serial to EFI variable after multiple attempts. It is recommended to power off the system and diagnose the hardware manually.")

					// Create log before exiting
					createOperationLog("Serial number update failed", false, baseSerial)

					fmt.Print("Poweroff system now? (Y/n): ")
					choice, _ := reader.ReadString('\n')
					choice = strings.TrimSpace(choice)
					if !strings.EqualFold(choice, "n") {
						fmt.Println("Powering off system...")
						_ = runCommandNoOutput("poweroff")
					} else {
						fmt.Println("Exiting without powering off. Please shutdown manually.")
					}
					os.Exit(1)
				}
				time.Sleep(500 * time.Millisecond) // Small delay between retries
				continue
			}

			// Successfully written, no need to check reading as it causes errors
			debugPrint("Successfully wrote serial number to EFI variable")
			serialWriteSuccess = true
			break
		}

		if !serialWriteSuccess {
			success = false
			criticalError("Failed to write serial to EFI variable after multiple attempts")
			os.Exit(1)
		}

		// If MAC was successfully set, also write it to EFI variable
		if !macAlreadySet || macAlreadySet {
			// Also write MAC to EFI variable regardless
			if err := writeMACToEfiVar(mac); err != nil {
				fmt.Printf(colorYellow+"[WARNING] Failed to write MAC to EFI variable: %v\n"+colorReset, err)
			} else {
				debugPrint("Successfully wrote MAC to EFI variable")
			}
		}

		// For compatibility with the old method, also write to file (всегда используем mbSN)
		if err := writeSerialToFile(mbSN); err != nil {
			fmt.Printf(colorYellow+"[WARNING] Failed to write serial to file: %v"+colorReset+"\n", err)
		} else {
			debugPrint(fmt.Sprintf("Successfully wrote mbSN=%s to SERIAL file", mbSN))
		}

		// Call bootctl function to set up one-time boot entry and reflash EFI
		if err := bootctl(); err != nil {
			success = false
			criticalError("Bootctl error: " + err.Error())
			os.Exit(1)
		}

		// Create log before reboot
		createOperationLog(actionPerformed, success, baseSerial)

		if success {
			successMessage("Serial number has been set successfully")
		}

		// Request system reboot
		fmt.Print("Serial number has been set. Reboot now? (Y/n): ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if strings.EqualFold(choice, "n") {
			fmt.Println("Please reboot manually to apply changes.")
		} else {
			fmt.Println("Rebooting system...")
			_ = runCommandNoOutput("reboot")
		}
	} else {
		// This case should never happen logically, but just in case
		fmt.Println(colorGreen + "No changes were required. Exiting..." + colorReset)
		createOperationLog("No changes required", true, baseSerial)
	}
}

// randomGUIDWithPrefix generates a GUID in the format 8-4-4-4-12 (hex), where
// the first 8 hex characters can be specified by prefix. The remaining blocks are generated randomly.
func randomGUIDWithPrefix(prefix string) (string, error) {
	// Prefix must consist of exactly 8 hex characters.
	// If empty, generate random 8.
	if prefix == "" {
		p, err := randomHex(4) // Generate 4 bytes => 8 hex characters
		if err != nil {
			return "", err
		}
		prefix = p
	} else {
		if len(prefix) != 8 {
			return "", errors.New("guid-prefix must have exactly 8 hex characters")
		}
		if _, err := hex.DecodeString(prefix); err != nil {
			return "", fmt.Errorf("invalid guid-prefix: %v", err)
		}
	}

	// Generate another 4-4-4-12 => 24 hex characters.
	suffixBytes, err := randomBytes(12)
	if err != nil {
		return "", fmt.Errorf("random generation error: %v", err)
	}

	suffixHex := hex.EncodeToString(suffixBytes) // 24 hex characters

	// Split into blocks: 4-4-4-12
	block1 := suffixHex[0:4]
	block2 := suffixHex[4:8]
	block3 := suffixHex[8:12]
	block4 := suffixHex[12:24]

	// Form GUID, returning string and nil
	guid := fmt.Sprintf("%s-%s-%s-%s-%s",
		strings.ToLower(prefix),
		block1,
		block2,
		block3,
		block4,
	)

	return guid, nil
}

// randomBytes reads n random bytes from crypto/rand
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// randomHex generates n random bytes and returns them in hex form
func randomHex(n int) (string, error) {
	b, err := randomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// writeSerialToEfiVar writes the serial number to an EFI variable
func writeSerialToEfiVar(serialNumber string) error {
	// Create a temporary file to pass data to efivar
	tmpFile, err := os.CreateTemp("", "serial-*.bin")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write the serial number to the temporary file
	if _, err := tmpFile.Write([]byte(serialNumber)); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write to temporary file: %v", err)
	}
	tmpFile.Close()

	// Full variable name
	varName := fmt.Sprintf("%s-%s", efiVarGUID, efiSNName)
	debugPrint("Writing to EFI variable: " + varName)

	// Run efivar to write the variable
	cmd := exec.Command(
		"efivar",
		"--write",
		"--name="+varName,
		"--attributes=7", // Non-volatile + BootService access + RuntimeService access = 7
		"--datafile="+tmpFile.Name(),
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to write EFI variable: %v (output: %s)", err, string(out))
	}

	fmt.Printf(colorGreen+"[INFO] Successfully wrote serial number to EFI variable '%s'\n"+colorReset, varName)
	return nil
}

// writeMACToEfiVar writes the MAC address to an EFI variable
func writeMACToEfiVar(macAddress string) error {
	// Create a temporary file to pass data to efivar
	tmpFile, err := os.CreateTemp("", "mac-*.bin")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// Write the MAC address to the temporary file
	if _, err := tmpFile.Write([]byte(macAddress)); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to write to temporary file: %v", err)
	}
	tmpFile.Close()

	// Full variable name
	varName := fmt.Sprintf("%s-%s", efiVarGUID, efiMACName)
	debugPrint("Writing to EFI variable: " + varName)

	// Run efivar to write the variable
	cmd := exec.Command(
		"efivar",
		"--write",
		"--name="+varName,
		"--attributes=7", // Non-volatile + BootService access + RuntimeService access = 7
		"--datafile="+tmpFile.Name(),
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to write EFI variable: %v (output: %s)", err, string(out))
	}

	fmt.Printf(colorGreen+"[INFO] Successfully wrote MAC address to EFI variable '%s'\n"+colorReset, varName)
	return nil
}

// writeSerialToFile writes the serial number to the SERIAL file for backward compatibility
func writeSerialToFile(serial string) error {
	filePath := filepath.Join(cDir, efiCont, serialFile)
	fmt.Printf("[INFO] Writing %s for compatibility...\n", filePath)
	return os.WriteFile(filePath, []byte(serial), 0644)
}

// clearEfiVariables removes all EFI variable files in /sys/firmware/efi/efivars/
// whose names start with varName + "-" (e.g. "SerialNumber-*")
func clearEfiVariables(varName string) error {
	// Path to the EFI variables directory
	efiVarsDir := "/sys/firmware/efi/efivars"

	// Read all entries in the directory
	entries, err := os.ReadDir(efiVarsDir)
	if err != nil {
		return fmt.Errorf("failed to read EFI variables directory %s: %v", efiVarsDir, err)
	}

	// The target prefix is varName followed by a dash
	targetPrefix := varName + "-"
	foundVariables := false

	fmt.Printf("[DEBUG] Looking for EFI variables starting with '%s'\n", targetPrefix)

	for _, entry := range entries {
		fileName := entry.Name()
		// Check if the variable file name starts with the target prefix
		if strings.HasPrefix(fileName, targetPrefix) {
			foundVariables = true
			fmt.Printf("[DEBUG] Found matching variable: %s\n", fileName)

			// Build the full file path in /sys/firmware/efi/efivars/
			filePath := filepath.Join(efiVarsDir, fileName)

			// First try to remove the immutable attribute using chattr
			chattrCmd := exec.Command("chattr", "-i", filePath)
			chattrOut, chattrErr := chattrCmd.CombinedOutput()
			if chattrErr != nil {
				fmt.Printf("[WARNING] Failed to remove immutable attribute from %s: %v\nOutput: %s\n",
					filePath, chattrErr, string(chattrOut))
				// Continue anyway - the file might not have the immutable attribute
			} else {
				fmt.Printf("[DEBUG] Removed immutable attribute from %s\n", filePath)
			}

			// Now attempt to delete the file
			if err := os.Remove(filePath); err != nil {
				fmt.Printf("[WARNING] Failed to remove EFI variable file %s: %v\n", filePath, err)

				// If direct deletion fails, try using rm command which might have more permissions
				rmCmd := exec.Command("rm", "-f", filePath)
				rmOut, rmErr := rmCmd.CombinedOutput()
				if rmErr != nil {
					fmt.Printf("[WARNING] Failed to remove EFI variable using rm command: %s: %v\nOutput: %s\n",
						filePath, rmErr, string(rmOut))
				} else {
					fmt.Printf("[INFO] Successfully removed EFI variable file: %s using rm command\n", filePath)
				}
			} else {
				fmt.Printf("[INFO] Successfully removed EFI variable file: %s\n", filePath)
			}
		}
	}

	if !foundVariables {
		fmt.Printf("[INFO] No existing EFI variables found for '%s'\n", varName)
	}

	return nil
}

// Function to create and save operation log
func createOperationLog(action string, success bool, originalSerial string) {
	fmt.Println(colorBlue + "Creating operation log..." + colorReset)

	// Get full dmidecode output
	dmidecodeOutput, err := runCommand("dmidecode")
	if err != nil {
		fmt.Printf(colorYellow+"[WARNING] Could not get dmidecode output for log: %v"+colorReset, err)
		dmidecodeOutput = "Error getting dmidecode output"
	}

	// Parse dmidecode output
	sections, err := parseDmidecodeOutput(dmidecodeOutput)
	if err != nil {
		fmt.Printf(colorYellow+"[WARNING] Could not parse dmidecode output: %v"+colorReset, err)
	}

	// Convert sections to a map for JSON
	systemInfo := make(map[string]interface{})
	for _, sec := range sections {
		key := sec.Title
		if key == "" {
			key = sec.Handle // Use handle if title is empty
		}

		sectionData := make(map[string]interface{})
		if sec.Handle != "" {
			sectionData["handle"] = sec.Handle
		}

		// Only include properties if they are not empty
		if len(sec.Properties) > 0 {
			sectionData["properties"] = sec.Properties
		}

		// If such a key already exists, convert value to an array or add to existing array
		if existing, exists := systemInfo[key]; exists {
			switch v := existing.(type) {
			case map[string]interface{}:
				// If we already have one section with this title, create an array of two elements
				systemInfo[key] = []interface{}{v, sectionData}
			case []interface{}:
				// If we already have an array of sections with this title, add to it
				systemInfo[key] = append(v, sectionData)
			default:
				// For other cases (e.g., if for some reason the value is not a map or slice)
				systemInfo[key] = []interface{}{existing, sectionData}
			}
		} else {
			// If the key doesn't exist yet, just add the section as is
			systemInfo[key] = sectionData
		}
	}

	// Create log data structure
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
		EfiSNVarName:    efiSNName,
		EfiMACVarName:   efiMACName,
		EfiVarGUID:      efiVarGUID,
	}

	// Convert to JSON
	jsonData, err := json.MarshalIndent(logData, "", "  ")
	if err != nil {
		fmt.Printf(colorYellow+"[WARNING] Could not create JSON log: %v"+colorReset, err)
		return
	}

	// Generate filename for the log
	timeFormat := time.Now().Format("060102150405") // YYMMDDHHMMSS
	filename := fmt.Sprintf("%s_%s-%s.json", productName, mbSN, timeFormat)

	// Save log to file if flag is set
	var logSaved bool = false
	var logRetries int = 0
	maxLogRetries := 3

	for !logSaved && logRetries < maxLogRetries {
		logRetries++

		if logToFile {
			logDir := filepath.Join(cDir, "logs")
			// Create log directory if it doesn't exist
			if _, err := os.Stat(logDir); os.IsNotExist(err) {
				if err := os.Mkdir(logDir, 0755); err != nil {
					fmt.Printf(colorYellow+"[WARNING] Could not create log directory: %v. Retry attempt %d/%d"+colorReset, err, logRetries, maxLogRetries)
					logDir = cDir
				}
			}

			logPath := filepath.Join(logDir, filename)
			if err := os.WriteFile(logPath, jsonData, 0644); err != nil {
				fmt.Printf(colorYellow+"[WARNING] Could not write log file: %v. Retry attempt %d/%d\n"+colorReset, err, logRetries, maxLogRetries)
				time.Sleep(500 * time.Millisecond) // Small delay between retries
			} else {
				fmt.Printf(colorGreen+"[INFO] Log saved to: %s\n"+colorReset, logPath)
				logSaved = true
			}
		} else {
			logSaved = true // Skip if logging to file is disabled
		}
	}

	if !logSaved && logToFile {
		// Final attempt to save locally in the current directory if all retries failed
		emergencyLogPath := filepath.Join(cDir, filename)
		if err := os.WriteFile(emergencyLogPath, jsonData, 0644); err != nil {
			criticalError("Failed to save log after multiple attempts. Final error: " + err.Error())
		} else {
			fmt.Printf(colorYellow+"[ATTENTION] Log could not be saved to logs directory after %d attempts. Emergency save to current directory: %s\n"+colorReset, maxLogRetries, emergencyLogPath)
			logSaved = true
		}
	}

	// Send log to server if specified
	var serverLogSent bool = false
	var serverRetries int = 0

	if logServer != "" {
		for !serverLogSent && serverRetries < maxLogRetries {
			serverRetries++

			// Create temporary file
			tempFile, err := os.CreateTemp("", "serial-log-*.json")
			if err != nil {
				fmt.Printf(colorYellow+"[WARNING] Could not create temporary file for log: %v. Retry attempt %d/%d"+colorReset, err, serverRetries, maxLogRetries)
				time.Sleep(500 * time.Millisecond)
				continue
			}

			// Write JSON to file
			if _, err := tempFile.Write(jsonData); err != nil {
				fmt.Printf(colorYellow+"[WARNING] Could not write to temporary file: %v. Retry attempt %d/%d"+colorReset, err, serverRetries, maxLogRetries)
				tempFile.Close()
				os.Remove(tempFile.Name())
				time.Sleep(500 * time.Millisecond)
				continue
			}
			tempFile.Close()

			// Parse server string to host and path
			var host, remotePath string
			parts := strings.SplitN(logServer, ":", 2)

			host = parts[0]
			if len(parts) > 1 {
				remotePath = parts[1]
			}

			// Create remote directory before sending file
			if remotePath != "" {
				// Remove trailing slash if present
				remotePath = strings.TrimSuffix(remotePath, "/")

				// Create directory on remote server
				mkdirCmd := exec.Command("ssh", host, "mkdir", "-p", remotePath)
				_, err := mkdirCmd.CombinedOutput()
				if err != nil {
					fmt.Printf(colorYellow+"[WARNING] Could not create remote directory: %v. Retry attempt %d/%d"+colorReset, err, serverRetries, maxLogRetries)
				}
			}

			// Build correct path for SCP
			var destination string
			if remotePath != "" {
				destination = fmt.Sprintf("%s:%s/%s", host, remotePath, filename)
			} else {
				destination = fmt.Sprintf("%s:%s", host, filename)
			}

			// Send file to server using SCP
			cmd := exec.Command("scp", tempFile.Name(), destination)
			output, err := cmd.CombinedOutput()

			// Clean up temporary file regardless of the result
			os.Remove(tempFile.Name())

			if err != nil {
				fmt.Printf(colorYellow+"[WARNING] Could not send log to server: %v\nOutput: %s\nRetry attempt %d/%d\n"+colorReset, err, output, serverRetries, maxLogRetries)
				time.Sleep(1 * time.Second) // Longer delay for network operations
			} else {
				fmt.Printf(colorGreen+"[INFO] Log sent to server: %s\n"+colorReset, destination)
				serverLogSent = true
				break
			}
		}

		if !serverLogSent {
			criticalError("Failed to send log to server " + logServer + " after multiple attempts")
		}
	}
}

// parseDmidecodeOutput parses dmidecode output and splits it into sections
func parseDmidecodeOutput(output string) ([]Section, error) {
	var sections []Section
	var currentSection *Section
	expectingTitle := false
	var currentPropKey string

	// Collect header lines (until the first line starting with "Handle")
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
				// If the header is not empty, add it as a section with "Header" title
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
				// Process the current line as the beginning of a section
			} else {
				headerLines = append(headerLines, line)
				continue
			}
		}

		// Start a new section
		if strings.HasPrefix(line, "Handle") {
			if currentSection != nil {
				// If title is empty, use handle value as title
				if currentSection.Title == "" {
					currentSection.Title = currentSection.Handle
				}
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

		// If title is expected, assign the current line as section title
		if expectingTitle {
			currentSection.Title = trimmed
			expectingTitle = false
			continue
		}

		// Process lines with properties
		if colonIndex := strings.Index(trimmed, ":"); colonIndex != -1 {
			key := strings.TrimSpace(trimmed[:colonIndex])
			value := strings.TrimSpace(trimmed[colonIndex+1:])
			if existing, ok := currentSection.Properties[key]; ok {
				// If the property already exists, convert it to an array
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
			// If the line does not contain a colon, assume it is a continuation of the previous property
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
		// If title is empty, use handle value as title
		if currentSection.Title == "" {
			currentSection.Title = currentSection.Handle
		}
		sections = append(sections, *currentSection)
	}
	return sections, nil
}

// bootctl mounts external EFI partition, copies contents of efishell directory (ctefi)
// and sets one-time boot entry (via setOneTimeBoot). Do not change this function!
func bootctl() error {
	// Determine boot device
	bootDev, err := findBootDevice()
	if err != nil {
		return fmt.Errorf("Could not determine boot device: %v", err)
	}

	// Find external EFI partition
	targetDevice, targetEfi, err := findExternalEfiPartition(bootDev)
	if err != nil || targetDevice == "" || targetEfi == "" {
		return errors.New("No external EFI partition found")
	}

	// Mount EFI partition to temporary directory
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

	// Copy contents of ctefi directory to root of mounted EFI partition
	cpCmd := fmt.Sprintf("cp -r %s/* %s", efiCont, mountPoint)
	if err := runCommandNoOutput("sh", "-c", cpCmd); err != nil {
		return fmt.Errorf("Failed to copy EFI content: %v", err)
	}
	debugPrint("Contents of " + efiCont + " copied to EFI partition.")

	// Call setOneTimeBoot function to create new entry and set BootNext
	if err := setOneTimeBoot(targetDevice, targetEfi); err != nil {
		_ = runCommandNoOutput("umount", mountPoint)
		return fmt.Errorf("setOneTimeBoot error: %v", err)
	}

	if err = runCommandNoOutput("bootctl", "set-oneshot", "03-efishell.conf"); err != nil {
		_ = runCommandNoOutput("umount", mountPoint)
		criticalError("Failed to set one-time boot entry: " + err.Error())
		os.Exit(1)
	} else {
		debugPrint("One-time boot entry set successfully.")
	}

	// Unmount EFI partition
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
	// Do not show full output, keep only debug messages
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
	// For NVMe devices, name looks like "/dev/nvme0n1p1" - parent disk: "/dev/nvme0n1"
	if strings.Contains(output, "nvme") {
		devRegex := regexp.MustCompile(`p[0-9]+$`)
		return devRegex.ReplaceAllString(output, ""), nil
	}
	// For other devices, e.g. "/dev/sda2" - parent disk: "/dev/sda"
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

	// Wait for extra (4th) line input, but no more than 500 ms.
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

// readLineWithTimeout tries to read a line from os.Stdin with a given timeout.
// Sets non-blocking mode on descriptor and performs cyclic check.
func readLineWithTimeout(timeout time.Duration) (string, error) {
	fd := int(os.Stdin.Fd())
	// Set non-blocking mode.
	if err := syscall.SetNonblock(fd, true); err != nil {
		return "", err
	}
	// Restore blocking mode when done.
	defer syscall.SetNonblock(fd, false)

	reader := bufio.NewReader(os.Stdin)
	deadline := time.Now().Add(timeout)
	for {
		// If there's at least one byte, read the whole line.
		_, err := reader.Peek(1)
		if err == nil {
			return reader.ReadString('\n')
		}
		// If time is up, stop waiting.
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

// writeMAcWithRetries tries to write MAC address with retries and driver recompilation if needed
func writeMAcWithRetries(macInput string) error {
	targetMAC := strings.ToLower(macInput)
	// If the specified MAC is already present, skip flashing
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

	// First attempt to load the driver as is
	driverErr := loadDriver()

	// If driver loading fails, try recompiling and loading again
	if driverErr != nil {
		fmt.Printf(colorYellow+"[WARNING] Initial driver load failed: %v\nAttempting to recompile driver..."+colorReset+"\n", driverErr)

		// Try to recompile the driver
		rtnicpgPath := filepath.Join(cDir, "rtnicpg")
		if info, err := os.Stat(rtnicpgPath); err == nil && info.IsDir() {
			if err := runCommandNoOutput("make", "-C", rtnicpgPath, "clean", "all"); err != nil {
				criticalError("Failed to recompile driver: " + err.Error())
				return err
			}
			fmt.Println(colorGreen + "[INFO] Driver recompilation successful." + colorReset)

			// Try loading the driver again after recompilation
			if driverErr = loadDriver(); driverErr != nil {
				criticalError("Failed to load driver even after recompilation: " + driverErr.Error())
				return driverErr
			}
		} else {
			criticalError("rtnicpg directory does not exist, cannot recompile driver")
			return fmt.Errorf("rtnicpg directory does not exist, cannot recompile driver")
		}
	}

	if err := os.Chmod(rtnic, 0755); err != nil {
		return fmt.Errorf("Failed to chmod %s: %v", rtnic, err)
	}

	modmac := strings.ReplaceAll(macInput, ":", "")
	fmt.Println(modmac)

	// Try to write MAC with retries
	var macWriteSuccess bool = false
	var macWriteErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		macWriteErr = runCommandNoOutput(rtnic, "/efuse", "/nodeid", modmac)

		if macWriteErr == nil {
			fmt.Println(colorGreen + "[INFO] MAC address was successfully written, verifying..." + colorReset)
			macWriteSuccess = true
			break
		} else {
			fmt.Printf(colorYellow+"[WARNING] Attempt %d: Failed to write MAC: %v\n"+colorReset, attempt, macWriteErr)

			if attempt == 1 {
				// On first failure, try to recompile the driver
				fmt.Println(colorYellow + "[WARNING] MAC write failed. Attempting to recompile driver and try again..." + colorReset)
				rtnicpgPath := filepath.Join(cDir, "rtnicpg")
				if info, err := os.Stat(rtnicpgPath); err == nil && info.IsDir() {
					if err := runCommandNoOutput("make", "-C", rtnicpgPath, "clean", "all"); err != nil {
						fmt.Printf(colorYellow+"[WARNING] Failed to recompile driver: %v\n"+colorReset, err)
					} else {
						fmt.Println(colorGreen + "[INFO] Driver recompilation successful." + colorReset)
						if err := loadDriver(); err != nil {
							fmt.Printf(colorYellow+"[WARNING] Failed to reload driver after recompilation: %v\n"+colorReset, err)
						}
					}
				}
			}

			time.Sleep(1 * time.Second) // Longer delay for hardware operations
		}
	}

	if !macWriteSuccess {
		criticalError("Failed to write MAC address after " + fmt.Sprintf("%d", maxRetries) + " attempts: " + macWriteErr.Error())
		return fmt.Errorf("Failed to write MAC address after %d attempts: %v", maxRetries, macWriteErr)
	}

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

			// Выключаем интерфейс
			_ = runCommandNoOutput("ip", "link", "set", "dev", newIface, "down")

			// Удаляем все IP-адреса с интерфейса
			_ = runCommandNoOutput("ip", "addr", "flush", "dev", newIface)

			// Устанавливаем MAC-адрес
			_ = runCommandNoOutput("ip", "link", "set", "dev", newIface, "address", targetMAC)

			// Включаем интерфейс
			_ = runCommandNoOutput("ip", "link", "set", "dev", newIface, "up")

			// Назначаем только оригинальный IP, не используем "replace" чтобы избежать добавления новых адресов
			assignErr = runCommandNoOutput("ip", "addr", "add", oldIP, "dev", newIface)

			if assignErr == nil {
				fmt.Printf(colorGreen+"[INFO] Interface %s restarted with IP %s\n"+colorReset, newIface, oldIP)
				break
			} else {
				fmt.Printf(colorYellow+"[WARNING] Attempt %d: Failed to assign IP %s to interface %s: %v\n"+colorReset, attempt, oldIP, newIface, assignErr)

				// Проверяем, не был ли уже назначен этот IP, так как это распространенная ошибка
				ipCheckOutput, _ := runCommand("ip", "addr", "show", "dev", newIface)
				if strings.Contains(ipCheckOutput, oldIP) {
					fmt.Printf(colorGreen+"[INFO] IP %s is already assigned to %s, continuing...\n"+colorReset, oldIP, newIface)
					assignErr = nil
					break
				}

				// Проверяем, не появились ли новые интерфейсы с нужным MAC
				if newIfaces, err := getInterfacesWithMAC(targetMAC); err == nil && len(newIfaces) > 0 {
					// Проверяем, не изменилось ли имя интерфейса
					foundDifferent := false
					for _, iface := range newIfaces {
						if iface != newIface {
							newIface = iface
							foundDifferent = true
							fmt.Printf("[INFO] Retrying with interface %s\n", newIface)
							break
						}
					}
					// Если не нашли новый интерфейс, продолжаем с текущим
					if !foundDifferent {
						fmt.Printf("[INFO] Still using interface %s\n", newIface)
					}
				} else {
					fmt.Println(colorYellow + "[WARNING] No interface with target MAC found on retry" + colorReset)
				}
			}

			if attempt == maxRetries && assignErr != nil {
				fmt.Printf(colorYellow+"[WARNING] Failed to assign IP after %d attempts: %v. Network configuration may need manual adjustment.\n"+colorReset, maxRetries, assignErr)
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

func loadDriver() error {
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
	// Get kernel version to include in driver filename
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

	// Check if the driver already exists and is loaded
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

	// If driver doesn't exist, compile it
	fmt.Printf("[INFO] Compiling module %s.\n", moduleDefault)
	if err := runCommandNoOutput("make", "-C", rtnicpgPath, "clean", "all"); err != nil {
		return fmt.Errorf("Compilation failed: %v", err)
	}
	fmt.Println("[INFO] Compilation completed successfully.")

	builtModule := filepath.Join(rtnicpgPath, moduleDefault+".ko")
	if _, err := os.Stat(builtModule); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Compiled module %s not found", builtModule)
	}

	// Rename the module if necessary
	if rtDrv != "" {
		err := os.Rename(builtModule, targetModulePath)
		if err != nil {
			return fmt.Errorf("Failed to rename %s to %s: %v", builtModule, targetModulePath, err)
		}
	} else {
		targetModulePath = builtModule
	}

	// Load the newly compiled module
	if err := runCommandNoOutput("insmod", targetModulePath); err != nil {
		return fmt.Errorf("Failed to load module %s: %v", targetModulePath, err)
	}
	fmt.Printf("[INFO] Module %s loaded successfully.\n", targetModulePath)
	return nil
}

// isModuleLoaded checks if a kernel module is already loaded
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

// setOneTimeBoot creates a new one-time boot entry and sets BootNext
func setOneTimeBoot(targetDevice, targetEfi string) error {
	// Use the regular expression that should not be changed - DO NOT TOUCH!
	re := regexp.MustCompile(`(?im)^Boot([0-9A-Fa-f]{4})(\*?)\s+OneTimeBoot\t(.+)$`)

	// Check if there are conflicting entries
	out, err := runCommand("efibootmgr")
	if err != nil {
		return fmt.Errorf("efibootmgr failed: %v", err)
	}

	// Find only entries that conflict (have the same boot path)
	matches := re.FindAllStringSubmatch(out, -1)

	// Define the boot path for our new entry
	targetBootPath := "\\EFI\\BOOT\\bootx64.efi"

	// Determine partition number for the new device
	var partition string
	// For NVMe devices, name looks like "/dev/nvme0n1p1" - parent disk: "/dev/nvme0n1"
	if strings.Contains(targetDevice, "nvme") {
		partition = strings.TrimPrefix(targetEfi, targetDevice+"p")
	} else {
		partition = strings.TrimPrefix(targetEfi, targetDevice)
	}
	if partition == "" {
		return errors.New("could not determine partition number from targetEfi")
	}

	// Remove only entries that conflict with our target entry
	for _, match := range matches {
		bootNum := match[1]

		// Get more detailed info about the entry
		bootInfo, err := runCommand("efibootmgr", "-v", "-b", bootNum)
		if err != nil {
			debugPrint(fmt.Sprintf("[WARNING] Failed to get info for Boot%s: %v", bootNum, err))
			continue
		}

		// Check if the entry contains the same boot path
		if strings.Contains(bootInfo, targetBootPath) {
			debugPrint("[INFO] Removing conflicting OneTimeBoot entry: Boot" + bootNum)
			if err := runCommandNoOutput("efibootmgr", "-B", "-b", bootNum); err != nil {
				debugPrint(fmt.Sprintf("[WARNING] Failed to remove Boot%s: %v", bootNum, err))
			}
		} else {
			debugPrint("[INFO] Keeping non-conflicting OneTimeBoot entry: Boot" + bootNum)
		}
	}

	debugPrint("targetDevice: " + targetDevice)

	debugPrint("[INFO] Creating new OneTimeBoot entry")
	// Create a new entry without displaying command result
	createCmd := exec.Command("efibootmgr",
		"-c",
		"-d", targetDevice,
		"-p", partition,
		"-L", "OneTimeBoot",
		"-l", targetBootPath)
	// Hide efibootmgr output, keep only debug messages
	var createOut bytes.Buffer
	createCmd.Stdout = &createOut
	createCmd.Stderr = &createOut
	if err := createCmd.Run(); err != nil {
		debugPrint("[ERROR] efibootmgr create output: " + createOut.String())
		return fmt.Errorf("failed to create new boot entry: %v", err)
	}

	// Find the created entry with OneTimeBoot label
	out, err = runCommand("efibootmgr", "-v")
	if err != nil {
		return fmt.Errorf("efibootmgr failed after creation: %v", err)
	}
	matches = re.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		return errors.New("new OneTimeBoot entry not found after creation")
	}

	// Find our new entry - it should be the last created with this label
	var bootNum string
	for _, match := range matches {
		candidateBootNum := match[1]
		bootInfo, err := runCommand("efibootmgr", "-v", "-b", candidateBootNum)
		if err == nil && strings.Contains(bootInfo, targetBootPath) &&
			strings.Contains(bootInfo, targetDevice) {
			bootNum = candidateBootNum
			break
		}
	}

	if bootNum == "" {
		// If we didn't find an exact match, use the last entry
		bootNum = matches[len(matches)-1][1]
	}

	debugPrint("[INFO] New OneTimeBoot entry created: Boot" + bootNum)

	// Set BootNext to the created entry
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
