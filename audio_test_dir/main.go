package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/mattn/go-tty"
)

// Parameters for speaker-test.
var speakerTestArgs = []string{"-t", "wav", "-c", "2", "-l", "1"}

// DeviceTest holds information about a device test.
type DeviceTest struct {
	Name  string // Device name.
	Sound string // Test result, e.g., "Passed", "Failed", or "Error".
}

// currentPrompt is the current prompt message displayed at the bottom.
var currentPrompt string

// refreshUI updates the screen every 250ms, displaying the test table and the current prompt.
func refreshUI(tests []DeviceTest, stop <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			printUI(tests, currentPrompt)
		}
	}
}

// printUI clears the screen and prints a table with information about the device tests.
func printUI(tests []DeviceTest, prompt string) {
	// ANSI escape sequences to clear the screen and move the cursor to the top-left.
	fmt.Print("\033[H\033[J")
	fmt.Println("=== Audio Output Testing ===")
	fmt.Println()
	fmt.Printf("%-3s | %-40s | %-30s\n", "No", "Device", "Sound")
	fmt.Println(strings.Repeat("-", 80))
	for i, d := range tests {
		name := d.Name
		if len(name) > 40 {
			name = name[:37] + "..."
		}
		fmt.Printf("%-3d | %-40s | %-30s\n", i+1, name, d.Sound)
	}
	fmt.Println()
	fmt.Println(prompt)
}

// listALSADevices runs "aplay -L" and selects only the default/active devices.
// It selects lines that contain "default" (case-insensitive) or begin with "sysdefault:" or "hdmi:".
func listALSADevices() ([]string, error) {
	cmd := exec.Command("aplay", "-L")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil // suppress errors

	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var devices []string
	lines := strings.Split(out.String(), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, ">") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "default") ||
			strings.HasPrefix(lower, "sysdefault:") ||
			strings.HasPrefix(lower, "hdmi:") {
			devices = append(devices, line)
		}
	}
	return devices, nil
}

// playSpeakerTestOnce runs speaker-test with the given parameters on the specified device.
// It uses exec.CommandContext so that the process can be killed when the context is canceled.
func playSpeakerTestOnce(ctx context.Context, device string) error {
	args := append(speakerTestArgs, "-D", device)
	cmd := exec.CommandContext(ctx, "speaker-test", args...)
	// Suppress command output.
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// testSimultaneous starts a background loop that continuously plays the test stereo signal using speaker-test,
// then asks the user for one overall answer: whether the sound is heard on the device.
// Once an answer is received, the context is canceled, which kills any running speaker-test process.
func testSimultaneous(device string) (result bool, err error) {
	currentPrompt = fmt.Sprintf("Device '%s': Testing both speakers simultaneously.\nPress Y if sound is heard, or N if not. (Esc/Ctrl+C to exit)", device)
	// Create a context to cancel the background loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)

	// Launch the background loop that plays the test signal.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				if err := playSpeakerTestOnce(ctx, device); err != nil {
					errChan <- err
					return
				}
			}
		}
	}()

	// Wait for valid user input.
	for {
		select {
		case err := <-errChan:
			return false, err
		default:
		}
		currentPrompt = fmt.Sprintf("Device '%s': Testing both speakers simultaneously.\nPress Y if sound is heard, or N if not. (Esc/Ctrl+C to exit)", device)
		r, exit, err := readSingleKey()
		if err != nil {
			return false, err
		}
		if exit {
			fmt.Println("\nExiting as requested by user.")
			syscall.Exit(0)
		}
		lower := strings.ToLower(string(r))
		if lower == "y" {
			return true, nil
		} else if lower == "n" {
			return false, nil
		}
		// Otherwise, repeat the input prompt.
	}
}

// readSingleKey reads one key using the github.com/mattn/go-tty library.
// If ESC (27) or Ctrl+C (3) is pressed, it returns an exit flag.
func readSingleKey() (rune, bool, error) {
	tty, err := tty.Open()
	if err != nil {
		return 0, false, err
	}
	defer tty.Close()

	r, err := tty.ReadRune()
	if err != nil {
		return 0, false, err
	}
	if r == 0x1B || r == 0x03 {
		return r, true, nil
	}
	// Only Y or N are accepted; other characters are ignored in the calling loop.
	return r, false, nil
}

func main() {
	// Retrieve the list of audio devices.
	devs, err := listALSADevices()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error obtaining device list: %v\n", err)
		os.Exit(1)
	}
	if len(devs) == 0 {
		fmt.Println("No default or active audio devices found.")
		os.Exit(0)
	}

	// Create a list for displaying test results.
	tests := make([]DeviceTest, len(devs))
	for i, d := range devs {
		tests[i] = DeviceTest{
			Name:  d,
			Sound: "Pending",
		}
	}

	// Start dynamic UI refresh.
	stopUI := make(chan struct{})
	go refreshUI(tests, stopUI)

	// Test each device sequentially.
	for i, d := range devs {
		tests[i].Sound = "Testing"
		currentPrompt = fmt.Sprintf("Device '%s': Testing both speakers simultaneously.", d)
		res, err := testSimultaneous(d)
		if err != nil {
			tests[i].Sound = "Error"
			fmt.Fprintf(os.Stderr, "Device '%s': testing error: %v\n", d, err)
			continue
		}
		if res {
			tests[i].Sound = "Passed"
		} else {
			tests[i].Sound = "Failed"
		}
	}

	// Stop UI refresh.
	close(stopUI)
	// Final UI render.
	printUI(tests, "Testing completed. Press any key to exit.")
	// Wait for any key press to exit.
	_, _, _ = readSingleKey()
}
