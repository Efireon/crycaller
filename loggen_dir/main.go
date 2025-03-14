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
	"path/filepath"
	"strings"
	"time"
)

// Config defines the structure for the JSON configuration.
type Config struct {
	Source string `json:"source"`
}

// Section represents a section from the dmidecode output.
type Section struct {
	Handle     string                 `json:"handle,omitempty"`
	Title      string                 `json:"title,omitempty"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// parseDmidecodeOutput parses the dmidecode output and splits it into sections.
func parseDmidecodeOutput(output string) ([]Section, error) {
	var sections []Section
	var currentSection *Section
	expectingTitle := false
	var currentPropKey string

	// Collect header lines (before the first line starting with "Handle")
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
				// If header is not empty, add it as a section with the title "Header".
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
				// Process the current line as the beginning of a section.
			} else {
				headerLines = append(headerLines, line)
				continue
			}
		}

		// Start a new section.
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

		// If the title is expected, assign the current line as the section title.
		if expectingTitle {
			currentSection.Title = trimmed
			expectingTitle = false
			continue
		}

		// Process lines with properties.
		if colonIndex := strings.Index(trimmed, ":"); colonIndex != -1 {
			key := strings.TrimSpace(trimmed[:colonIndex])
			value := strings.TrimSpace(trimmed[colonIndex+1:])
			if existing, ok := currentSection.Properties[key]; ok {
				// If the property already exists, convert it into a slice.
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
			// If the line does not contain a colon, assume it is a continuation of the previous property.
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

// getDmidecodeOutput obtains the dmidecode output based on the provided source:
// - If the source is empty, it runs the local "dmidecode" command.
// - If the source is an existing file (and not a directory), it reads its contents.
// - If the source contains "@", it executes dmidecode on a remote host via SSH.
// - Otherwise, it assumes the source is the path to an executable.
func getDmidecodeOutput(source string) (string, error) {
	if source == "" {
		cmd := exec.Command("dmidecode")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("failed to run dmidecode locally: %v, output: %s", err, string(output))
		}
		return string(output), nil
	}

	if info, err := os.Stat(source); err == nil && !info.IsDir() {
		data, err := ioutil.ReadFile(source)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}

	if strings.Contains(source, "@") {
		cmd := exec.Command("ssh", source, "dmidecode")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("failed to run dmidecode on remote host: %v, output: %s", err, string(output))
		}
		return string(output), nil
	}

	// If the source is not a file and does not contain "@", assume it's a path to an executable.
	cmd := exec.Command(source)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run dmidecode from specified source: %v, output: %s", err, string(output))
	}
	return string(output), nil
}

func main() {
	// Flags:
	// -c: path to the JSON configuration (e.g. {"source": "user@192.168.1.100"} or {"source": "/path/to/file.txt"}).
	// -s: either a source (user@ip, path to file/executable) or a directory where the result should be saved.
	// -sn: path to a file that contains the serial number to use.
	configPath := flag.String("c", "", "Path to the JSON configuration (contains the 'source' field)")
	sourceFlag := flag.String("s", "", "Source: user@ip, path to file/executable, or a directory to save the result")
	serialNumberFile := flag.String("sn", "", "Path to a file containing the serial number")
	flag.Parse()

	var config Config
	if *configPath != "" {
		data, err := ioutil.ReadFile(*configPath)
		if err != nil {
			log.Fatalf("Error reading configuration file: %v", err)
		}
		if err := json.Unmarshal(data, &config); err != nil {
			log.Fatalf("Error parsing configuration file: %v", err)
		}
	}

	var source string
	var outputDir string

	// If the -s flag points to an existing directory, use it as the output destination
	// and run the local command to obtain the data.
	if *sourceFlag != "" {
		if info, err := os.Stat(*sourceFlag); err == nil && info.IsDir() {
			outputDir = *sourceFlag
			source = ""
		} else {
			source = *sourceFlag
		}
	} else if config.Source != "" {
		if info, err := os.Stat(config.Source); err == nil && info.IsDir() {
			outputDir = config.Source
			source = ""
		} else {
			source = config.Source
		}
	} else {
		source = ""
	}

	output, err := getDmidecodeOutput(source)
	if err != nil {
		log.Fatalf("Error obtaining dmidecode output: %v", err)
	}

	sections, err := parseDmidecodeOutput(output)
	if err != nil {
		log.Fatalf("Error parsing dmidecode output: %v", err)
	}

	// Extract data for generating the filename:
	// - From the "System Information" section, retrieve the "Product" field (e.g., INFERIT)
	systemProduct := "UNKNOWN"
	for _, sec := range sections {
		titleLower := strings.ToLower(sec.Title)
		if strings.Contains(titleLower, "system information") {
			for key, val := range sec.Properties {
				if strings.ToLower(key) == "product" || strings.ToLower(key) == "product name" {
					if str, ok := val.(string); ok && str != "" {
						systemProduct = strings.ReplaceAll(str, " ", "")
					}
				}
			}
		}
	}

	// Get the baseboard serial number:
	// If a file is specified via -sn, use its content.
	baseboardSerial := "UNKNOWN"
	if *serialNumberFile != "" {
		data, err := ioutil.ReadFile(*serialNumberFile)
		if err != nil {
			log.Fatalf("Error reading serial number file: %v", err)
		}
		baseboardSerial = strings.TrimSpace(string(data))
	} else {
		// Otherwise, extract it from the "Base Board Information" section.
		for _, sec := range sections {
			titleLower := strings.ToLower(sec.Title)
			if strings.Contains(titleLower, "base board information") {
				for key, val := range sec.Properties {
					if strings.ToLower(key) == "serial number" {
						if str, ok := val.(string); ok && str != "" {
							baseboardSerial = strings.ReplaceAll(str, " ", "")
						}
					}
				}
			}
		}
	}

	// Generate timestamp (YYMMDDHHMMSS)
	timestamp := time.Now().Format("060102150405")
	filename := fmt.Sprintf("%s_%s-%s.json", systemProduct, baseboardSerial, timestamp)

	// Instead of an array, create a JSON object where the key is the section title.
	finalData := make(map[string]interface{})
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
		// If a key already exists, convert the value into a slice.
		if existing, exists := finalData[key]; exists {
			switch v := existing.(type) {
			case []interface{}:
				finalData[key] = append(v, sectionData)
			default:
				finalData[key] = []interface{}{v, sectionData}
			}
		} else {
			finalData[key] = sectionData
		}
	}

	jsonData, err := json.MarshalIndent(finalData, "", "  ")
	if err != nil {
		log.Fatalf("Error converting to JSON: %v", err)
	}

	// If an output directory is specified, save the JSON to a file with the generated filename.
	if outputDir != "" {
		fullPath := filepath.Join(outputDir, filename)
		if err := ioutil.WriteFile(fullPath, jsonData, 0644); err != nil {
			log.Fatalf("Error writing file: %v", err)
		}
		fmt.Printf("Data saved in %s\n", fullPath)
	} else {
		// Otherwise, print the JSON to standard output.
		fmt.Println(string(jsonData))
	}
}
