package main

import (
    "flag"
    "image"
    "image/color"
    "log"
    "os"
    "os/signal"
    "syscall"
    "time"

    "gocv.io/x/gocv"
)

func main() {
    // Flags:
    // -model: path to the Haar Cascade model file
    // -d: detection mode; if set, the program exits when a face is detected,
    //     and if 'E' is pressed, it exits with an error.
    // -n: silent mode; disables the display of the camera feed.
    modelFile := flag.String("model", "haarcascade_frontalface_default.xml", "Path to the Haar Cascade model file")
    detectionMode := flag.Bool("d", false, "If set, the program exits when a face is detected; if 'E' is pressed, exits with error")
    noDisplay := flag.Bool("n", false, "Disable camera feed display (silent mode)")
    flag.Parse()

    // Check if the model file exists
    if _, err := os.Stat(*modelFile); os.IsNotExist(err) {
        log.Fatalf("Model file '%s' not found", *modelFile)
    }

    // Load the Haar Cascade model
    classifier := gocv.NewCascadeClassifier()
    defer classifier.Close()
    if !classifier.Load(*modelFile) {
        log.Fatalf("Error loading model '%s'", *modelFile)
    }

    // Open the camera
    webcam, err := gocv.OpenVideoCapture(0)
    if err != nil {
        log.Fatalf("Error opening camera: %v", err)
    }
    defer webcam.Close()

    // Create a window for video display if display is not disabled
    var window *gocv.Window
    if !*noDisplay {
        window = gocv.NewWindow("Object Detection")
        defer window.Close()
    }

    // Matrix for the frame
    img := gocv.NewMat()
    defer img.Close()

    // Signal handling (Ctrl+C, SIGTERM)
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
    go func() {
        <-sigChan
        log.Println("Terminating...")
        os.Exit(0)
    }()

    // Detection update interval (update every 200 ms)
    detectionDelay := 200 * time.Millisecond
    lastDetectionTime := time.Now()
    var lastRects []image.Rectangle

    log.Println("Starting object detection...")
    for {
        // Read frame from the camera
        if ok := webcam.Read(&img); !ok || img.Empty() {
            log.Println("Failed to capture frame from camera")
            continue
        }

        // Update detection if the specified time interval has passed
        if time.Since(lastDetectionTime) >= detectionDelay {
            gray := gocv.NewMat()
            gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)
            lastRects = classifier.DetectMultiScale(gray)
            gray.Close()
            lastDetectionTime = time.Now()

            // If detection mode is enabled and a face is detected, exit successfully
            if *detectionMode && len(lastRects) > 0 {
                log.Println("Face detected, exiting successfully")
                os.Exit(0)
            }
        }

        // Draw rectangles around detected objects (and log the detection)
        for _, r := range lastRects {
            log.Printf("Detected object: x=%d, y=%d, width=%d, height=%d\n", r.Min.X, r.Min.Y, r.Size().X, r.Size().Y)
            gocv.Rectangle(&img, r, color.RGBA{0, 255, 0, 0}, 2)
        }

        // If display is enabled, show the frame and handle key events
        if !*noDisplay {
            window.IMShow(img)
            key := window.WaitKey(1)
            if *detectionMode {
                // In detection mode: if the 'E' key is pressed, exit with an error
                if key == int('E') {
                    log.Println("Key 'E' pressed, exiting with error")
                    os.Exit(1)
                }
            } else {
                // In normal mode: exit on any key press
                if key >= 0 {
                    break
                }
            }
        } else {
            // Silent mode: add a brief sleep to avoid high CPU usage
            time.Sleep(1 * time.Millisecond)
        }
    }
}
