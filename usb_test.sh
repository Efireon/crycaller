#!/usr/bin/env bash

# usb_test.sh - USB Storage Device Testing Script
# This script detects USB storage devices based on a configuration file,
# performs read and write speed tests automatically upon connection,
# and records the results in a JSON file. It supports quick checks,
# custom configuration files, and repeated testing with multiple devices.

# Default configuration and results files
DEFAULT_CONFIG_FILE="ports.json"
DEFAULT_RESULTS_FILE="test_results.json"

# Initialize variables for command-line arguments
CONFIG_FILE="$DEFAULT_CONFIG_FILE"
RESULTS_FILE="$DEFAULT_RESULTS_FILE"
SETUP_MODE=false
AUTO_CONFIRM=false
QUICK_TEST=false
REPEAT_COUNT=1
ALL_MODE=false

# Initialize associative arrays to track connection counts and device identifiers
declare -A CONNECTION_COUNTS
declare -A DEVICE_IDS

# Usage information
usage() {
  echo "Usage: $0 [-s] [-f] [-c CONFIG_FILE|ALL] [-r COUNT] [-y] [-h]"
  echo "  -s                 : Setup mode for creating/updating the configuration (automatic port detection and selection)"
  echo "  -f                 : Quick check mode (writes a 10 MB file without measuring speed)"
  echo "  -c CONFIG_FILE|ALL : Specify a custom configuration file or use ALL to test all detected ports"
  echo "  -r COUNT           : Repeat the test COUNT times for each port"
  echo "  -y                 : Automatic confirmation for data writing (no prompt)"
  echo "  -h                 : Display this help message"
  exit 1
}

# Parse command-line arguments
while getopts ":sfc:r:yh" opt; do
  case ${opt} in
    s )
      SETUP_MODE=true
      ;;
    f )
      QUICK_TEST=true
      ;;
    c )
      if [[ "$OPTARG" == "ALL" ]]; then
        ALL_MODE=true
      else
        CONFIG_FILE="$OPTARG"
      fi
      ;;
    r )
      if [[ "$OPTARG" =~ ^[1-9][0-9]*$ ]]; then
        REPEAT_COUNT="$OPTARG"
      else
        echo "Invalid repeat count: $OPTARG" >&2
        usage
      fi
      ;;
    y )
      AUTO_CONFIRM=true
      ;;
    h )
      usage
      ;;
    \? )
      echo "Invalid option: -$OPTARG" >&2
      usage
      ;;
    : )
      echo "Option -$OPTARG requires an argument." >&2
      usage
      ;;
  esac
done
shift $((OPTIND -1))

# Function to check installed dependencies
check_dependencies() {
  local dependencies=("jq" "hdparm" "udevadm" "lsblk" "dd" "mktemp" "grep" "awk" "mount" "umount" "readlink" "pv" "lsusb")
  for cmd in "${dependencies[@]}"; do
    if ! command -v "$cmd" &> /dev/null; then
      echo "The '$cmd' utility is required but not installed." >&2
      if [[ "$cmd" == "hdparm" || "$cmd" == "pv" || "$cmd" == "jq" ]]; then
        echo "Installing '$cmd'..." >&2
        sudo pacman -S --noconfirm "$cmd"
      else
        echo "Please install '$cmd' manually." >&2
        exit 1
      fi
    fi
  done
}

# Function to ensure the script is run as root
check_root() {
  if [[ $EUID -ne 0 ]]; then
    echo "This script must be run as root (use sudo)." >&2
    exit 1
  fi
}

# Function to detect USB ports using lsusb -t
detect_ports_lsusb() {
  lsusb -t | awk '
    /^\/:  Bus [0-9]+\./ {
      split($2, bus_port, ":")
      bus = bus_port[1]
      port = bus_port[2]
      speed = $NF
      # Convert speed to numeric value (remove M or G)
      gsub(/[A-Za-z]/, "", speed)
      if (index($NF, "G") > 0) {
        speed_num = speed * 1000
      } else {
        speed_num = speed
      }
      if (speed_num >= 10000) {
        bus_num = substr($2, 6, index($2, ":") - 6)
        port_num = substr($2, index($2, ":") + 1, length($2))
        print "Bus " bus_num ".Port " port_num
      }
    }
  '
}

# Function to retrieve device information
get_device_info() {
  local device="$1"
  local description product vendor driver maxpower speed

  description=$(udevadm info --query=property --name="$device" | grep "^ID_TYPE=" | cut -d'=' -f2)
  product=$(udevadm info --query=property --name="$device" | grep "^ID_MODEL=" | cut -d'=' -f2)
  vendor=$(udevadm info --query=property --name="$device" | grep "^ID_VENDOR=" | cut -d'=' -f2)
  driver=$(udevadm info --query=property --name="$device" | grep "^ID_USB_DRIVER=" | cut -d'=' -f2)
  maxpower=$(udevadm info --query=property --name="$device" | grep "^ID_USB_MAXPOWER=" | cut -d'=' -f2)
  speed=$(udevadm info --query=property --name="$device" | grep "^ID_USB_SPEED=" | cut -d'=' -f2)

  configuration="driver=${driver:-Unknown} maxpower=${maxpower:-Unknown} speed=${speed:-Unknown}"

  echo "$description" "$product" "$vendor" "$configuration"
}

# Function to generate a unique device identifier
generate_device_id() {
  local bus_port="$1"
  local device="$2"
  # Using device serial or UUID if available
  local id=$(udevadm info --query=property --name="$device" | grep "^ID_SERIAL=" | cut -d'=' -f2)
  if [[ -z "$id" ]]; then
    id=$(udevadm info --query=property --name="$device" | grep "^ID_USB_SERIAL_SHORT=" | cut -d'=' -f2)
  fi
  if [[ -z "$id" ]]; then
    # Fallback to device path
    id="$bus_port"
  fi
  echo "$id"
}

# Function to perform write speed test
test_write_speed() {
  local mount_point="$1"
  local temp_file="$mount_point/test_write_speed.tmp"

  # Proceed without user confirmation if AUTO_CONFIRM is enabled
  if [[ "$AUTO_CONFIRM" == false && "$QUICK_TEST" == false ]]; then
    echo "WARNING: 396 MB of data will be written to the device at $mount_point."
    echo "Proceeding without user confirmation."
  fi

  # Check write access
  touch "$mount_point/test_write_access.tmp" 2>/dev/null
  if [[ $? -ne 0 ]]; then
    echo "Device is read-only. Write test cannot be performed." >&2
    return 1
  fi
  rm -f "$mount_point/test_write_access.tmp"

  if [[ "$QUICK_TEST" == true ]]; then
    # Quick test: write 10 MB without measuring speed
    dd if=/dev/zero of="$temp_file" bs=1M count=10 conv=fdatasync 2>/dev/null
    if [[ $? -ne 0 ]]; then
      echo "Error during quick write test." >&2
      rm -f "$temp_file"
      return 1
    fi
    rm -f "$temp_file"
    echo "Quick write test completed (10 MB written)."
    return 0
  fi

  # Full test: write 396 MB and capture the final write speed
  write_result=$(dd if=/dev/zero bs=1M count=396 | pv -s 396M | dd of="$temp_file" conv=fdatasync 2>&1 | grep -oP '\d+(\.\d+)? MB/s' | tail -1 | awk '{print $1}')

  # Verify if the write was successful
  if [[ -z "$write_result" ]]; then
    echo "Error during write speed test." >&2
    rm -f "$temp_file"
    return 1
  fi

  # Remove the temporary file
  rm -f "$temp_file"

  echo "$write_result"
}

# Function to perform read speed test
test_read_speed() {
  local device="$1"

  # Use hdparm to measure read speed
  read_result=$(hdparm -Tt "$device" 2>/dev/null | grep -E 'Timing buffered disk reads' | awk -F'=' '{print $2}' | awk '{print $1}')

  # Check if read speed was successfully retrieved
  if [[ -z "$read_result" ]]; then
    echo "Unknown"
    return 1
  fi

  echo "$read_result"
}

# Function to perform tests and record results
perform_tests() {
  local bus_port="$1"
  local port="$2"
  local device_path
  local device
  local mount_point
  local description product vendor configuration
  local size
  local write_speed read_speed
  local write_speed_num read_speed_num
  local avg_speed
  local timestamp
  local needs_unmount=false

  echo "----------------------------------------"
  echo "Testing port: $port (BusPort: $bus_port)"

  # Find the device connected to this Bus:Port
  device_path=$(lsusb -t | grep -E "^/$bus_port:" | awk '{print $NF}')

  if [[ -z "$device_path" ]]; then
    echo "No device found at $bus_port." >&2
    echo "----------------------------------------"
    return
  fi

  # Find the corresponding block device using lsblk and device path
  device=$(lsblk -no NAME,TRAN | grep "usb" | awk '{print "/dev/"$1}')

  if [[ -z "$device" ]]; then
    echo "Failed to identify the block device connected to port $port." >&2
    echo "----------------------------------------"
    return
  fi

  # Check if the device is already mounted
  mount_point=$(lsblk -no MOUNTPOINT "$device" | grep "^/")
  
  if [[ -z "$mount_point" ]]; then
    # Check for partitions
    partitions=($(lsblk -ln -o NAME "$device" | grep "^$(basename "$device")[0-9]"))
    
    if [[ ${#partitions[@]} -gt 0 ]]; then
      # Use the first partition for testing
      partition="/dev/${partitions[0]}"
      mount_point=$(mktemp -d /mnt/usb_test.XXXX)
      
      sudo mount "$partition" "$mount_point" 2>/dev/null
      if [[ $? -ne 0 ]]; then
        echo "Failed to mount partition $partition to $mount_point. Skipping testing." >&2
        rm -rf "$mount_point"
        echo "----------------------------------------"
        return
      fi
      
      echo "Device mounted at $mount_point"
      needs_unmount=true
    else
      # If no partitions, try mounting the device directly
      mount_point=$(mktemp -d /mnt/usb_test.XXXX)
      
      sudo mount "$device" "$mount_point" 2>/dev/null
      if [[ $? -ne 0 ]]; then
        echo "Failed to mount device $device directly to $mount_point. Skipping testing." >&2
        rm -rf "$mount_point"
        echo "----------------------------------------"
        return
      fi
      
      echo "Device mounted directly at $mount_point"
      needs_unmount=true
    fi
  else
    echo "Device already mounted at $mount_point"
    needs_unmount=false
  fi

  # Retrieve device information
  read description product vendor configuration <<< $(get_device_info "$device")

  # Replace empty fields with "Unknown"
  description=${description:-"Unknown"}
  product=${product:-"Unknown"}
  vendor=${vendor:-"Unknown"}
  configuration=${configuration:-"Unknown"}

  # Get device size
  size=$(lsblk -b -d -o SIZE -n "$device" | awk '{printf "%.2f GB", $1/1024/1024/1024}')

  echo "Description: $description"
  echo "Model: $product"
  echo "Vendor: $vendor"
  echo "Configuration: $configuration"
  echo "Size: $size"

  # Perform tests based on mode
  if [[ "$QUICK_TEST" == true ]]; then
    echo "Performing quick write test..."
    test_write_speed "$mount_point"
    echo "Quick write test completed."
  else
    echo "Performing write test..."
    write_speed=$(test_write_speed "$mount_point")
    if [[ $? -ne 0 || -z "$write_speed" ]]; then
      echo "Failed to measure write speed." >&2
      write_speed="Unknown"
    else
      echo "Write speed: $write_speed MB/s"
    fi

    echo "Performing read test..."
    read_speed=$(test_read_speed "$device")
    if [[ "$read_speed" == "Unknown" ]]; then
      echo "Failed to measure read speed." >&2
      read_speed="Unknown"
    else
      echo "Read speed: $read_speed MB/s"
    fi

    # Extract numerical speed values for calculations
    write_speed_num=$(echo "$write_speed" | grep -o '^[0-9.]*')
    read_speed_num=$(echo "$read_speed" | grep -o '^[0-9.]*')

    # Verify that speed values were obtained
    if [[ -z "$write_speed_num" || -z "$read_speed_num" ]]; then
      echo "Speed testing was not completed successfully. Results will not be recorded." >&2
      # If the device was mounted by the script, unmount it
      if [[ "$needs_unmount" == true ]]; then
        sudo umount "$mount_point"
        echo "Device unmounted from $mount_point"
        rm -rf "$mount_point"
      fi
      echo "----------------------------------------"
      return
    fi

    # Get the current date and time
    timestamp=$(date +"%Y-%m-%d %H:%M:%S")

    # Calculate average speed
    avg_speed=$(awk "BEGIN {printf \"%.2f\", ($write_speed_num + $read_speed_num)/2}")

    # Define minimum and maximum speeds (since we have single measurements)
    min_write_speed="$write_speed_num"
    min_read_speed="$read_speed_num"
    max_write_speed="$write_speed_num"
    max_read_speed="$read_speed_num"

    # Formulate configuration string with correct speed conversion
    # Assume USB speed in Mbit/s = read_speed_num (MB/s) * 8
    speed_mbit=$(awk "BEGIN {printf \"%.2f\", $read_speed_num * 8}")
    config_str="driver=${driver:-Unknown} maxpower=${maxpower:-Unknown} speed=${speed_mbit}Mbit/s"

    # Add results to the JSON output, grouping speeds into a "speed" object
    jq --arg port "$port" \
       --arg device "$(basename "$device")" \
       --arg description "$description" \
       --arg product "$product" \
       --arg vendor "$vendor" \
       --arg volume "$size" \
       --arg configuration "$config_str" \
       --arg timestamp "$timestamp" \
       --arg write_speed "$write_speed_num" \
       --arg read_speed "$read_speed_num" \
       --arg avg_speed "$avg_speed" \
       --arg min_write_speed "$min_write_speed" \
       --arg min_read_speed "$min_read_speed" \
       --arg max_write_speed "$max_write_speed" \
       --arg max_read_speed "$max_read_speed" \
       --arg connection_count "${CONNECTION_COUNTS[$port]}" \
       '.results += [{
         "port": $port,
         "device": $device,
         "description": $description,
         "product": $product,
         "vendor": $vendor,
         "configuration": $configuration,
         "timestamp": $timestamp,
         "volume": $volume,
         "connection_count": ($connection_count | tonumber),
         "speed": {
           "write_speed_MBps": ($write_speed | tonumber),
           "read_speed_MBps": ($read_speed | tonumber),
           "average_speed_MBps": ($avg_speed | tonumber),
           "min_write_speed_MBps": ($min_write_speed | tonumber),
           "min_read_speed_MBps": ($min_read_speed | tonumber),
           "max_write_speed_MBps": ($max_write_speed | tonumber),
           "max_read_speed_MBps": ($max_read_speed | tonumber)
         }
       }]' "$RESULTS_FILE" > "${RESULTS_FILE}.tmp" && mv "${RESULTS_FILE}.tmp" "$RESULTS_FILE"
  fi

  # Increment connection count
  CONNECTION_COUNTS["$port"]=$((CONNECTION_COUNTS["$port"] + 1))

  # Unmount the device if it was mounted by the script
  if [[ "$needs_unmount" == true ]]; then
    sudo umount "$mount_point"
    echo "Device unmounted from $mount_point"
    rm -rf "$mount_point"
  fi

  echo "Testing of port $port completed. ($CONNECTION_COUNTS[$port]/$REPEAT_COUNT)"
  echo "----------------------------------------"
}

# Function to perform repeated tests
repeat_tests() {
  local repetitions="$1"
  local is_quick="$2"
  local current_repetition=1

  echo "Starting repeated testing: $repetitions repetitions for each port."

  while [[ $current_repetition -le $repetitions ]]; do
    echo "### Repetition $current_repetition of $repetitions ###"
    for port in "${PORTS[@]}"; do
      # Check if the port has already completed all repetitions
      if [[ "${CONNECTION_COUNTS[$port]}" -ge "$repetitions" ]]; then
        continue
      fi

      # Check if the device is connected
      if [[ -e "$port" ]]; then
        # Generate device ID to prevent multiple tests on the same connection
        device_path=$(lsusb -t | grep -E "^/$port:" | awk '{print $NF}')
        device=$(lsblk -no NAME,TRAN | grep "usb" | awk '{print "/dev/"$1}')
        device_id=$(generate_device_id "$port" "$device")

        # If the device ID has changed, it means a new connection
        if [[ "${DEVICE_IDS[$port]}" != "$device_id" ]]; then
          DEVICE_IDS["$port"]="$device_id"
          echo "Device connected to $port. Starting test."
          perform_tests "$port" "$port"
        fi
      fi
    done

    ((current_repetition++))
    sleep 1
  done

  echo "All specified ports have been tested $repetitions times."
}

# Function to setup configuration
setup_configuration() {
  echo "==== CONFIGURATION CREATION/UPDATE MODE ===="
  echo "Automatically searching for available ports matching the pattern Bus X.Port Y, excluding non-storage devices..."
  echo

  # Get the list of detected ports
  mapfile -t DETECTED_PORTS < <(detect_ports_lsusb)

  # Check if any ports were found
  if [[ ${#DETECTED_PORTS[@]} -eq 0 ]]; then
    echo "No USB ports found matching the criteria." >&2
    echo "Ensure that devices are present (insert USB Type-C) or that the system recognizes them." >&2
    exit 0
  fi

  echo "Found the following ports:"
  for i in "${!DETECTED_PORTS[@]}"; do
    echo "[$i] ${DETECTED_PORTS[$i]}"
  done

  echo
  echo "Select the ports you want to test by entering their numbers separated by spaces."
  echo "For example: 0 2 3"
  echo "Or press Enter without input to cancel configuration creation/update."

  read -p "Enter the port numbers to include in the config: " SELECTED

  # Check if the user entered anything
  if [[ -z "$SELECTED" ]]; then
    echo "Configuration creation/update canceled."
    exit 0
  fi

  # Convert input into an array of indices
  IFS=' ' read -ra SELECTED_INDEXES <<< "$SELECTED"

  # Validate the entered indices
  VALID=true
  for idx in "${SELECTED_INDEXES[@]}"; do
    if ! [[ "$idx" =~ ^[0-9]+$ ]] || (( idx < 0 )) || (( idx >= ${#DETECTED_PORTS[@]} )); then
      echo "Invalid port number: $idx" >&2
      VALID=false
    fi
  done

  if [[ "$VALID" == "false" ]]; then
    echo "Please run the script again and enter valid port numbers." >&2
    exit 1
  fi

  # Collect the selected ports
  NEW_PORTS=()
  for idx in "${SELECTED_INDEXES[@]}"; do
    NEW_PORTS+=( "${DETECTED_PORTS[$idx]}" )
  done

  # Create a JSON string using jq for proper formatting
  JSON_ARRAY=$(printf '%s\n' "${NEW_PORTS[@]}" | jq -R . | jq -s .)
  JSON_STRING="{\"ports\": $JSON_ARRAY}"

  # Save to the config file
  echo
  echo "The final list of ports will be saved to $CONFIG_FILE:"
  echo "$JSON_STRING" | jq .  # Pretty print for readability
  echo "$JSON_STRING" > "$CONFIG_FILE"

  echo "Configuration successfully saved."
}

# Function to perform a quick test
quick_test_mode() {
  echo "==== QUICK TEST MODE ===="
  echo "Monitoring configured ports and performing quick write tests (10 MB) upon device connection."
  echo

  while true; do
    for port in "${PORTS[@]}"; do
      if [[ -e "$port" ]]; then
        # Generate device ID to prevent multiple tests on the same connection
        device_path=$(lsusb -t | grep -E "^/$port:" | awk '{print $NF}')
        device=$(lsblk -no NAME,TRAN | grep "usb" | awk '{print "/dev/"$1}')
        device_id=$(generate_device_id "$port" "$device")

        # If the device ID has changed, it means a new connection
        if [[ "${DEVICE_IDS[$port]}" != "$device_id" ]]; then
          DEVICE_IDS["$port"]="$device_id"
          echo "Device connected to $port. Starting quick test."
          perform_tests "$port" "$port"
        fi
      fi
    done
    sleep 2
  done
}

# Main Script Execution

# Initial checks
check_root
check_dependencies

# Setup mode
if [[ "$SETUP_MODE" == true ]]; then
  setup_configuration
  exit 0
fi

# Handling ALL mode
if [[ "$ALL_MODE" == true ]]; then
  echo "=== ALL MODE: TESTING ALL DETECTED PORTS ==="
  # Detect all current ports with connected devices
  mapfile -t PORTS < <(detect_ports_lsusb)

  # Check if any ports are detected
  if [[ ${#PORTS[@]} -eq 0 ]]; then
    echo "No USB ports detected for testing." >&2
    exit 1
  fi
else
  # Check if a custom config file exists
  if [[ ! -f "$CONFIG_FILE" ]]; then
    echo "Configuration file $CONFIG_FILE not found!" >&2
    echo "Run:  $0 -s" >&2
    echo "To create the configuration (list of ports)." >&2
    exit 1
  fi

  # Read the array of ports from JSON
  mapfile -t PORTS < <(jq -r '.ports[]' "$CONFIG_FILE" 2>/dev/null)
  if [[ ${#PORTS[@]} -eq 0 ]]; then
    echo "The file '$CONFIG_FILE' does not have a valid .ports field or the list is empty." >&2
    echo "Run: $0 -s   to create/update the configuration." >&2
    exit 1
  fi
fi

echo "Monitoring configured ports for device connections..."
echo "Press [CTRL+C] to stop the script."

# Initialize connection counts and device IDs
for port in "${PORTS[@]}"; do
  CONNECTION_COUNTS["$port"]=0
  DEVICE_IDS["$port"]=""
done

# Loop to monitor ports
while true; do
  for port in "${PORTS[@]}"; do
    if [[ -e "$port" ]]; then
      # Generate device ID to prevent multiple tests on the same connection
      device_path=$(lsusb -t | grep -E "^/$port:" | awk '{print $NF}')
      device=$(lsblk -no NAME,TRAN | grep "usb" | awk '{print "/dev/"$1}')
      device_id=$(generate_device_id "$port" "$device")

      # If the device ID has changed, it means a new connection
      if [[ "${DEVICE_IDS[$port]}" != "$device_id" ]]; then
        DEVICE_IDS["$port"]="$device_id"
        echo "Device connected to $port. Starting test."
        perform_tests "$port" "$port"
      fi
    else
      # If device was previously connected, reset device ID
      if [[ -n "${DEVICE_IDS[$port]}" ]]; then
        echo "Device disconnected from $port."
        DEVICE_IDS["$port"]=""
      fi
    fi
  done

  # Check if repeat count is specified and met
  all_completed=true
  for port in "${PORTS[@]}"; do
    if [[ "${CONNECTION_COUNTS[$port]}" -lt "$REPEAT_COUNT" ]]; then
      all_completed=false
      break
    fi
  done

  if [[ "$all_completed" == true ]]; then
    echo "All specified ports have been tested $REPEAT_COUNT times."
    echo "Test results saved in $RESULTS_FILE."
    exit 0
  fi

  sleep 2
done
