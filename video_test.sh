#!/bin/bash

# Script for Testing Video Output Ports

# Default configuration file path
CONFIG_FILE="./video_cfg.json"

# Colors for improved readability
RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

# Function to list all available video ports
list_ports() {
    # Directory containing DRM connectors
    drm_path="/sys/class/drm/"
    if [[ ! -d "$drm_path" ]]; then
        echo -e "${RED}Path $drm_path does not exist. Ensure that DRM is supported on your system.${NC}"
        exit 1
    fi

    # Extract connector names, including the 'cardX-' prefix
    ports=()
    for connector in "$drm_path"card*/*; do
        connector_basename=$(basename "$connector")

        # Remove the 'cardX-' prefix to get the connector type
        connector_name=$(echo "$connector_basename" | sed 's/^card[0-9]*-//')

        # Filter only real connectors (e.g., HDMI, VGA, DP, eDP, DVI, USB-C, Thunderbolt)
        if [[ "$connector_name" =~ ^(HDMI|VGA|DP|eDP|DVI|USB-C|Thunderbolt) ]]; then
            ports+=("$connector_basename")
        fi
    done

    # Remove duplicates and sort
    unique_ports=($(printf "%s\n" "${ports[@]}" | sort -u -V))
    echo "${unique_ports[@]}"
}

# Function to set ports (-s)
set_ports() {
    local mode="$1"  # Mode: "test" (default), "work", "ALL", "CON"

    if [[ "$mode" == "ALL" ]]; then
        echo "Saving all available video ports without user selection..."
        ports=($(list_ports))
        if [[ ${#ports[@]} -eq 0 ]]; then
            echo -e "${RED}No video ports found.${NC}"
            exit 1
        fi

        # Create JSON configuration with 'test': false for all ports
        jq -n --argjson ports "$(printf '%s\n' "${ports[@]}" | jq -R . | jq -s .)" \
            '{video_ports: ($ports | map({name: ., test: false}))}' > "$CONFIG_FILE"

        echo -e "${GREEN}Configuration saved to $CONFIG_FILE${NC}"
        return
    fi

    if [[ "$mode" == "CON" ]]; then
        echo "Saving all connected video ports without user selection..."
        ports=($(list_ports))
        connected_ports=()

        for port in "${ports[@]}"; do
            status_file="/sys/class/drm/$port/status"
            if [[ -f "$status_file" ]]; then
                status=$(cat "$status_file")
                if [[ "$status" == "connected" ]]; then
                    connected_ports+=("$port")
                fi
            fi
        done

        if [[ ${#connected_ports[@]} -eq 0 ]]; then
            echo -e "${RED}No connected video ports found.${NC}"
            exit 1
        fi

        # Create JSON configuration with 'test': true for all connected ports
        jq -n --argjson ports "$(printf '%s\n' "${connected_ports[@]}" | jq -R . | jq -s .)" \
            '{video_ports: ($ports | map({name: ., test: true}))}' > "$CONFIG_FILE"

        echo -e "${GREEN}Configuration saved to $CONFIG_FILE${NC}"
        return
    fi

    # For modes "test" and "work"
    echo "Available video ports:"
    ports=($(list_ports))
    if [[ ${#ports[@]} -eq 0 ]]; then
        echo -e "${RED}No video ports found.${NC}"
        exit 1
    fi

    # Display ports with numbering
    for i in "${!ports[@]}"; do
        # Remove the 'cardX-' prefix for display
        display_name=$(echo "${ports[$i]}" | sed 's/^card[0-9]*-//')
        echo "$((i+1)). $display_name"
    done

    echo -n "Select ports for testing (enter numbers separated by space): "
    read -a selected_numbers

    selected_ports=()
    for num in "${selected_numbers[@]}"; do
        if [[ "$num" =~ ^[0-9]+$ ]] && [[ $num -ge 1 && $num -le ${#ports[@]} ]]; then
            selected_ports+=("${ports[$((num-1))]}")
        else
            echo -e "${RED}Invalid port number: $num${NC}"
            exit 1
        fi
    done

    if [[ ${#selected_ports[@]} -eq 0 ]]; then
        echo -e "${RED}No ports selected for testing.${NC}"
        exit 1
    fi

    # Create JSON configuration based on mode
    if [[ "$mode" == "work" ]]; then
        # Set 'test' to false for all selected ports
        jq -n --argjson ports "$(printf '%s\n' "${selected_ports[@]}" | jq -R . | jq -s .)" \
            '{video_ports: ($ports | map({name: ., test: false}))}' > "$CONFIG_FILE"
    else
        # Set 'test' to true for all selected ports
        jq -n --argjson ports "$(printf '%s\n' "${selected_ports[@]}" | jq -R . | jq -s .)" \
            '{video_ports: ($ports | map({name: ., test: true}))}' > "$CONFIG_FILE"
    fi

    echo -e "${GREEN}Configuration saved to $CONFIG_FILE${NC}"
}

# Function to check ports (-c)
check_ports() {
    if [[ ! -f "$CONFIG_FILE" ]]; then
        echo -e "${RED}Configuration file $CONFIG_FILE not found. Use -s to create one.${NC}"
        exit 1
    fi

    # Read configuration
    mapfile -t ports < <(jq -c '.video_ports[]' "$CONFIG_FILE")

    if [[ ${#ports[@]} -eq 0 ]]; then
        echo -e "${RED}No ports found in the configuration.${NC}"
        exit 1
    fi

    echo "Checking video ports from configuration:"

    for port_json in "${ports[@]}"; do
        # Extract port name and test flag
        port_name=$(echo "$port_json" | jq -r '.name')
        port_test=$(echo "$port_json" | jq -r '.test')

        # Get display-friendly port name
        display_port=$(echo "$port_name" | sed 's/^card[0-9]*-//')

        if [[ "$port_test" == "true" ]]; then
            # In test mode: check if port is connected and prompt user
            status_file="/sys/class/drm/$port_name/status"
            if [[ -f "$status_file" ]]; then
                status=$(cat "$status_file")
                if [[ "$status" == "connected" ]]; then
                    echo -n "Is there output on port $display_port? (y/n): "
                    read -r answer
                    case "$answer" in
                        [Yy]* )
                            echo -e "${GREEN}Port $display_port confirmed.${NC}"
                            ;;
                        [Nn]* )
                            echo -e "${RED}Port $display_port NOT confirmed.${NC}"
                            echo -e "\nTest FAILED."
                            echo -e "Failed ports:"
                            echo "- $display_port (not confirmed)"
                            exit 1
                            ;;
                        * )
                            echo -e "${RED}Invalid input. Skipping port $display_port.${NC}"
                            echo -e "\nTest FAILED."
                            echo -e "Failed ports:"
                            echo "- $display_port (invalid input)"
                            exit 1
                            ;;
                    esac
                else
                    echo -e "${RED}ERROR: Port $display_port is NOT connected.${NC}"
                    echo -e "\nTest FAILED."
                    echo -e "Failed ports:"
                    echo "- $display_port (not connected)"
                    exit 1
                fi
            else
                echo -e "${RED}Cannot determine the status of port $display_port.${NC}"
                echo -e "\nTest FAILED."
                echo -e "Failed ports:"
                echo "- $display_port (status unknown)"
                exit 1
            fi
        elif [[ "$port_test" == "false" ]]; then
            # In notest mode: check only if port exists in the system
            if list_ports | grep -qw "$port_name"; then
                echo -e "${GREEN}Port $display_port exists in the system.${NC}"
            else
                echo -e "${RED}ERROR: Port $display_port does NOT exist in the system.${NC}"
                echo -e "\nTest FAILED."
                echo -e "Failed ports:"
                echo "- $display_port (does not exist)"
                exit 1
            fi
        else
            echo -e "${RED}Unknown test mode for port $display_port.${NC}"
            echo -e "\nTest FAILED."
            echo -e "Failed ports:"
            echo "- $display_port (unknown test mode)"
            exit 1
        fi
    done

    echo -e "\n${GREEN}All ports passed the tests.${NC}"
    exit 0
}

# Function to display the number of video ports
count_ports() {
    ports=($(list_ports))
    echo "Number of video ports: ${#ports[@]}"
}

# Function to display help (-h)
show_help() {
    echo "Usage: $0 [-s [work|ALL|CON]] [-c] [-h]"
    echo "  -s [work|ALL|CON]    Set ports and save to config."
    echo "                        If 'work' is specified, ports are marked as 'notest'."
    echo "                        If 'ALL' is specified, all system video ports are saved without selection."
    echo "                        If 'CON' is specified, all connected video ports are saved without selection and marked as 'test: true'."
    echo "  -c                    Check ports based on the config."
    echo "  -h                    Display this help message."
}

# Display the number of video ports if no arguments are provided
if [[ $# -eq 0 ]]; then
    count_ports
    exit 0
fi

# Parse arguments
while getopts ":s::ch" opt; do
    case $opt in
        s)
            # Check if the optional argument is provided and is not another option
            if [[ -n "$OPTARG" && ! "$OPTARG" =~ ^- ]]; then
                if [[ "$OPTARG" == "work" || "$OPTARG" == "ALL" || "$OPTARG" == "CON" ]]; then
                    set_ports "$OPTARG"
                else
                    echo -e "${RED}Invalid argument for -s: $OPTARG${NC}"
                    show_help
                    exit 1
                fi
            else
                # If no argument is provided, assume 'test' mode
                set_ports "test"
                # Note: Due to getopts limitations, arguments after -s without a value might be treated as new options
                # It's recommended to use '-s work', '-s ALL', or '-s CON'
            fi
            ;;
        c)
            check_ports
            ;;
        h)
            show_help
            exit 0
            ;;
        \?)
            echo -e "${RED}Invalid option: -$OPTARG${NC}" >&2
            show_help
            exit 1
            ;;
        :)
            # No action needed for optional arguments
            ;;
    esac
done

# Display the number of video ports by default
count_ports
