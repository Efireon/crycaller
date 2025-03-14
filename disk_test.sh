#!/bin/bash

# Function to display help
usage() {
    echo "Usage: $0 [-d expected_count] [-q]"
    echo "  -d <number>    Check that the number of devices equals <number>."
    echo "  -q             Quiet mode. Used only with -d."
    exit 1
}

# Initialize option variables
expected_count=""
quiet_mode=0

# Parse arguments
while getopts ":d:q" opt; do
  case ${opt} in
    d )
      expected_count=$OPTARG
      ;;
    q )
      quiet_mode=1
      ;;
    \? )
      echo "Unknown option: -$OPTARG" >&2
      usage
      ;;
    : )
      echo "Option -$OPTARG requires an argument." >&2
      usage
      ;;
  esac
done
shift $((OPTIND -1))

# Validate the -d argument if provided
if [[ ! -z "$expected_count" && ! "$expected_count" =~ ^[0-9]+$ ]]; then
    echo "Error: The value for -d must be a number." >&2
    usage
fi

# Function to exclude virtual devices
is_virtual_device() {
    local dev="$1"
    # Exclude devices starting with zram, loop, or dm-
    if [[ "$dev" =~ ^(zram|loop|dm-) ]]; then
        return 0
    fi
    return 1
}

# Function to get the connection port of a USB device
get_usb_ports() {
    local device_path="$1"
    local name=$(basename "$device_path")

    # Find all symlinks in /dev/disk/by-path/ pointing to the device
    local symlinks
    symlinks=$(find /dev/disk/by-path/ -lname "../../$name" 2>/dev/null)

    local ports=()

    for symlink in $symlinks; do
        # Extract PCI bus number and USB port information
        # Example: pci-0000:04:00.4-usb-0:2:1.0-scsi-0:0:0:0
        # Or: pci-0000:04:00.4-usbv2-0:2:1.0-scsi-0:0:0:0
        if [[ "$symlink" =~ pci-0000:([0-9]{2}):[0-9]{2}\.[0-9]+-(usbv?[0-9]*)-([0-9]+:[0-9]+:[0-9]+\.[0-9]+) ]]; then
            pci_bus="${BASH_REMATCH[1]}"
            # Remove leading zeros from PCI bus number
            pci_bus=$(echo "$pci_bus" | sed 's/^0*//')
            usb_port="${BASH_REMATCH[3]}"
            # Replace '.' with ':' in USB port
            usb_port="${usb_port//./:}"
            # Format as 'usb(4:0:2:1:0)'
            formatted_port="usb(${pci_bus}:${usb_port})"
            ports+=("$formatted_port")
        fi
    done

    # Remove duplicates
    unique_ports=($(printf "%s\n" "${ports[@]}" | sort -u))

    # Join ports into a tab-separated string
    echo "${unique_ports[*]}"
}

# Function to get the connection port of an NVMe device
get_nvme_ports() {
    local device_path="$1"
    local name=$(basename "$device_path")

    # Find all symlinks in /dev/disk/by-path/ pointing to the device
    local symlinks
    symlinks=$(find /dev/disk/by-path/ -lname "../../$name" 2>/dev/null)

    local ports=()

    for symlink in $symlinks; do
        # Extract PCI bus number and NVMe identifier
        # Example: pci-0000:04:00.0-nvme-1
        if [[ "$symlink" =~ pci-0000:([0-9]{2}):[0-9]{2}\.[0-9]+-nvme-([0-9]+) ]]; then
            pci_bus="${BASH_REMATCH[1]}"
            # Remove leading zeros from PCI bus number
            pci_bus=$(echo "$pci_bus" | sed 's/^0*//')
            nvme_id="${BASH_REMATCH[2]}"
            # Format as 'nvme(4:1)'
            formatted_port="nvme(${pci_bus}:${nvme_id})"
            ports+=("$formatted_port")
        fi
    done

    # Remove duplicates
    unique_ports=($(printf "%s\n" "${ports[@]}" | sort -u))

    # Join ports into a tab-separated string
    echo "${unique_ports[*]}"
}

# Function to get information about storage devices
get_devices_info() {
    # Use lsblk with -P for paired fields, including LABEL
    lsblk -dn -P -o NAME,TRAN,MODEL,SIZE,LABEL | while read -r line; do
        # Extract values
        name=$(echo "$line" | grep -oP 'NAME="\K[^"]+')
        tran=$(echo "$line" | grep -oP 'TRAN="\K[^"]+')
        model=$(echo "$line" | grep -oP 'MODEL="\K[^"]+')
        size=$(echo "$line" | grep -oP 'SIZE="\K[^"]+')
        label=$(echo "$line" | grep -oP 'LABEL="\K[^"]+')

        # Exclude virtual devices
        if is_virtual_device "$name"; then
            continue
        fi

        # Skip devices without TRAN
        if [[ -z "$tran" ]]; then
            continue
        fi

        device="/dev/$name"
        type="$tran"

        # Get connection port information
        if [[ "$tran" == "usb" ]]; then
            ports=$(get_usb_ports "$device")
            ports=${ports:-"Unknown"}
        elif [[ "$tran" == "nvme" ]]; then
            ports=$(get_nvme_ports "$device")
            ports=${ports:-"Unknown"}
        else
            ports="N/A"
        fi

        # Check model, size, and label
        model=${model:-"Unknown"}
        size=${size:-"Unknown"}
        label=${label:-"None"}

        # Add device information in tab-separated format
        echo -e "${device}\t${type}\t${ports}\t${model}\t${label}\t${size}"
    done
}

# Get device information and count
device_info=$(get_devices_info)
device_count=$(echo "$device_info" | grep -c "^/dev/")

# Function to perform count check
check_count() {
    if [[ "$device_count" -eq "$expected_count" ]]; then
        return 0
    else
        return 1
    fi
}

# Main script logic
if [[ $quiet_mode -eq 1 && ! -z "$expected_count" ]]; then
    # Quiet mode with count check
    check_count
    exit $?
elif [[ $quiet_mode -eq 0 ]]; then
    # Normal mode output
    if [[ $device_count -gt 0 ]]; then
        echo "Detected devices: $device_count"
        echo ""

        # Output device information without headers using 'column'
        echo -e "$device_info" | column -t -s $'\t'

        echo ""
    else
        echo "No storage devices detected."
    fi

    # Count check if -d was provided
    if [[ ! -z "$expected_count" ]]; then
        if check_count; then
            echo "Check passed: $device_count devices detected as expected."
            exit 0
        else
            echo "Check failed: $device_count devices detected, expected $expected_count."
            exit 1
        fi
    fi
fi

exit 0
