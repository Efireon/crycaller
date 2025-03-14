#!/bin/bash

# Script Name: webcam_qr_stream.sh
# Description: Processes webcam video stream in real-time to detect QR codes.

# Parameters
OUTPUT_DIR="$HOME/webcam_qr_logs"
LOG_FILE="$OUTPUT_DIR/webcam_qr.log"
IMAGE_PREFIX="$OUTPUT_DIR/qr_image"
VIDEO_DEVICE="/dev/video0"
VIDEO_SIZE="1280x720"
INPUT_FORMAT="mjpeg"  # Change to 'yuyv422' or others if needed
COOLDOWN=5  # Time in seconds between captures for the same QR code

# Create the results directory if it does not exist
mkdir -p "$OUTPUT_DIR"

# Logging function
log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') - $1" | tee -a "$LOG_FILE"
}

log "Starting webcam QR code detection."

# Check for the presence of the video device
if [ ! -e "$VIDEO_DEVICE" ]; then
    log "Webcam not found: $VIDEO_DEVICE."
    exit 1
fi

log "Device found: $VIDEO_DEVICE"

# Check if the user is part of the 'video' group
if ! groups "$(whoami)" | grep -q "\bvideo\b"; then
    log "User $(whoami) is not in the 'video' group. Adding..."
    sudo usermod -aG video "$(whoami)"
    log "Added $(whoami) to 'video' group. Please log out and log back in for changes to take effect."
    exit 1
fi

# Initialize variables to prevent repeated captures of the same QR code
LAST_QR=""
LAST_CAPTURE_TIME=0

# Function to capture an image from the webcam
capture_image() {
    local qr_data="$1"
    local timestamp
    timestamp=$(date '+%Y%m%d_%H%M%S')
    local image_file="${IMAGE_PREFIX}_${timestamp}.jpg"

    log "Capturing image for QR code: $qr_data"
    ffmpeg -f v4l2 -input_format "$INPUT_FORMAT" -video_size "$VIDEO_SIZE" -i "$VIDEO_DEVICE" -vframes 1 "$image_file" -y >/dev/null 2>>"$LOG_FILE"

    if [ $? -eq 0 ] && [ -f "$image_file" ]; then
        log "Image successfully captured: $image_file"
    else
        log "Failed to capture image."
    fi
}

# Infinite loop to continuously capture and scan frames
while true; do
    # Capture a single frame and pipe it to zbarimg
    QR_RESULT=$(ffmpeg -f v4l2 -input_format "$INPUT_FORMAT" -video_size "$VIDEO_SIZE" -i "$VIDEO_DEVICE" -vframes 1 -f image2pipe - | zbarimg --quiet --raw --stdin 2>>"$LOG_FILE")

    if [ -n "$QR_RESULT" ]; then
        current_time=$(date +%s)
        time_diff=$((current_time - LAST_CAPTURE_TIME))

        # Check if the same QR code was detected recently
        if [ "$QR_RESULT" != "$LAST_QR" ] || [ "$time_diff" -ge "$COOLDOWN" ]; then
            log "Detected QR Code: $QR_RESULT"
            capture_image "$QR_RESULT"
            LAST_QR="$QR_RESULT"
            LAST_CAPTURE_TIME=$current_time
        else
            log "Repeated QR Code detected: $QR_RESULT. Skipping capture."
        fi
    fi

    # Sleep briefly to reduce CPU usage
    sleep 1
done
