#!/bin/bash

# Function to display usage information
usage() {
    echo "Usage: $0 [options]"
    echo "Options:"
    echo "  -i <count>          Specifies the expected number of network interfaces (only for verification)"
    echo "  -c <config>         Path to the configuration file for verification (default: net_config.json)"
    echo "  -s <options>        Creates a configuration from the current system with options:"
    echo "                      vendor  - exclude the Vendor field from the configuration"
    echo "                      product - exclude the Product field from the configuration"
    echo "                      port    - do not check the port where the interface is located"
    echo "                      ping    - ignore the ping result during verification"
    echo "                      Options can be combined with commas, for example: vendor,port"
    echo ""
    echo "Examples:"
    echo "  Create a configuration ignoring port and ping:"
    echo "    $0 -s \"port,ping\""
    echo "  Verify a configuration with the expected number of interfaces:"
    echo "    $0 -i 2 -c net_config.json"
    exit 1
}

# Function to check for required utilities
check_dependencies() {
    for cmd in lshw ethtool ip ping jq; do
        if ! command -v "$cmd" &> /dev/null; then
            echo "Required utility '$cmd' is not installed. Please install it."
            exit 1
        fi
    done
}

# Function to create the configuration
create_config() {
    OUTPUT_FILE="$CONFIG_FILE"  # Use the path to the configuration file specified by -c or default

    echo "Creating configuration file '$OUTPUT_FILE'..."

    # Get the list of network interfaces in JSON format
    INTERFACES_JSON=$(sudo lshw -class network -json)
    if [[ $? -ne 0 || -z "$INTERFACES_JSON" ]]; then
        echo "Error executing lshw. Ensure you have the necessary permissions."
        exit 1
    fi

    # Get the router IP (default gateway)
    ROUTER_IP=$(ip route | awk '/default/ {print $3}' | head -n1)
    if [[ -z "$ROUTER_IP" ]]; then
        echo "Failed to determine the router IP (default gateway)."
        exit 1
    fi

    # Prepare options
    SKIP_VENDOR=false
    SKIP_PRODUCT=false
    SKIP_PORT=false
    SKIP_PING=false
    IFS=',' read -ra OPTIONS <<< "$SET_OPTIONS"
    for option in "${OPTIONS[@]}"; do
        case $option in
            vendor)
                SKIP_VENDOR=true
                ;;
            product)
                SKIP_PRODUCT=true
                ;;
            port)
                SKIP_PORT=true
                ;;
            ping)
                SKIP_PING=true
                ;;
            *)
                echo "Unknown option: $option"
                usage
                ;;
        esac
    done

    # Collect interface information
    INTERFACES_ARRAY=$(echo "$INTERFACES_JSON" | jq -c '.[]' | while read -r iface; do
        NAME=$(echo "$iface" | jq -r '.logicalname // "Unknown"')
        VENDOR=$(echo "$iface" | jq -r '.vendor // "Unknown"')
        PRODUCT=$(echo "$iface" | jq -r '.product // "Unknown"')
        SPEED=$(ethtool "$NAME" 2>/dev/null | awk '/Speed:/ {print $2}' || echo "Unknown")
        PORT=$(echo "$iface" | jq -r '.businfo // "Unknown"')

        # Check router availability
        if ping -c 1 -W 1 "$ROUTER_IP" > /dev/null 2>&1; then
            PING_RESULT="Success"
        else
            PING_RESULT="Fail"
        fi

        # Apply -s options
        [[ $SKIP_VENDOR == true ]] && VENDOR="*"
        [[ $SKIP_PRODUCT == true ]] && PRODUCT="*"
        [[ $SKIP_PORT == true ]] && PORT="*"
        [[ $SKIP_PING == true ]] && PING_RESULT="*"

        # Create JSON object for the interface
        jq -n \
            --arg vendor "$VENDOR" \
            --arg product "$PRODUCT" \
            --arg speed "$SPEED" \
            --arg port "$PORT" \
            --arg ping "$PING_RESULT" \
            '{
                Vendor: $vendor,
                Product: $product,
                Speed: $speed,
                Port: $port,
                Ping: $ping
            }'
    done | jq -s '.')

    # Get the total number of interfaces
    TOTAL_COUNT=$(echo "$INTERFACES_ARRAY" | jq 'length')

    # Create the final JSON
    FINAL_JSON=$(jq -n \
        --argjson count "$TOTAL_COUNT" \
        --argjson interfaces "$INTERFACES_ARRAY" \
        '{
            count: $count,
            interfaces: $interfaces
        }')

    # Validate JSON
    echo "$FINAL_JSON" | jq . >/dev/null 2>&1
    if [[ $? -ne 0 ]]; then
        echo "Error forming the JSON configuration."
        exit 1
    fi

    # Write to file
    echo "$FINAL_JSON" > "$OUTPUT_FILE"

    echo "Configuration successfully created in file '$OUTPUT_FILE'."
}

# Function to verify the configuration
check_config() {
    if [[ -z "$CONFIG_FILE" ]]; then
        echo "Path to the configuration file not specified. Use the -c option."
        exit 1
    fi

    if [[ ! -f "$CONFIG_FILE" ]]; then
        echo "Configuration file '$CONFIG_FILE' not found."
        exit 1
    fi

    # Read the configuration
    CONFIG_JSON=$(cat "$CONFIG_FILE")
    if ! echo "$CONFIG_JSON" | jq . >/dev/null 2>&1; then
        echo "Configuration file contains invalid JSON."
        exit 1
    fi

    # Extract the count from the configuration
    CONFIG_COUNT=$(echo "$CONFIG_JSON" | jq '.count')

    # If -i is specified, override the count from the configuration
    if [[ $EXPECTED_COUNT -gt 0 ]]; then
        EXPECTED_TOTAL_COUNT=$EXPECTED_COUNT
    else
        EXPECTED_TOTAL_COUNT=$(echo "$CONFIG_JSON" | jq '.count')
    fi

    # Get the current list of network interfaces in JSON format
    CURRENT_INTERFACES_JSON=$(sudo lshw -class network -json)
    if [[ $? -ne 0 || -z "$CURRENT_INTERFACES_JSON" ]]; then
        echo "Error executing lshw. Ensure you have the necessary permissions."
        exit 1
    fi

    # Convert current interfaces to an array
    mapfile -t CURRENT_INTERFACES < <(echo "$CURRENT_INTERFACES_JSON" | jq -c '.[]')

    # Check the number of network interfaces
    ACTUAL_COUNT=${#CURRENT_INTERFACES[@]}
    if [[ $ACTUAL_COUNT -ne $EXPECTED_TOTAL_COUNT ]]; then
        echo "Number of network interfaces does not match. Expected: $EXPECTED_TOTAL_COUNT, Found: $ACTUAL_COUNT"
        exit 1
    fi

    # Extract the interfaces from the configuration
    CONFIG_INTERFACES=$(echo "$CONFIG_JSON" | jq -c '.interfaces[]')

    # Iterate over each interface in the configuration
    while read -r config_iface; do
        CONFIG_VENDOR=$(echo "$config_iface" | jq -r '.Vendor')
        CONFIG_PRODUCT=$(echo "$config_iface" | jq -r '.Product')
        CONFIG_SPEED=$(echo "$config_iface" | jq -r '.Speed')
        CONFIG_PORT=$(echo "$config_iface" | jq -r '.Port')
        CONFIG_PING=$(echo "$config_iface" | jq -r '.Ping')

        MATCH_FOUND=false

        for current_iface in "${CURRENT_INTERFACES[@]}"; do
            CURRENT_VENDOR=$(echo "$current_iface" | jq -r '.vendor // "Unknown"')
            CURRENT_PRODUCT=$(echo "$current_iface" | jq -r '.product // "Unknown"')
            CURRENT_NAME=$(echo "$current_iface" | jq -r '.logicalname // "Unknown"')
            CURRENT_SPEED=$(ethtool "$CURRENT_NAME" 2>/dev/null | awk '/Speed:/ {print $2}' || echo "Unknown")
            CURRENT_PORT=$(echo "$current_iface" | jq -r '.businfo // "Unknown"')

            # Check ping if not ignored
            if [[ "$CONFIG_PING" != "*" ]]; then
                ROUTER_IP=$(ip route | awk '/default/ {print $3}' | head -n1)
                if ping -c 1 -W 1 "$ROUTER_IP" > /dev/null 2>&1; then
                    CURRENT_PING="Success"
                else
                    CURRENT_PING="Fail"
                fi
            fi

            # Compare fields
            # Compare Vendor
            if [[ "$CONFIG_VENDOR" != "*" && "$CONFIG_VENDOR" != "$CURRENT_VENDOR" ]]; then
                continue
            fi

            # Compare Product
            if [[ "$CONFIG_PRODUCT" != "*" && "$CONFIG_PRODUCT" != "$CURRENT_PRODUCT" ]]; then
                continue
            fi

            # Compare Speed
            if [[ "$CONFIG_SPEED" != "*" && "$CONFIG_SPEED" != "$CURRENT_SPEED" ]]; then
                continue
            fi

            # Compare Port
            if [[ "$CONFIG_PORT" != "*" && "$CONFIG_PORT" != "$CURRENT_PORT" ]]; then
                continue
            fi

            # Compare Ping
            if [[ "$CONFIG_PING" != "*" ]]; then
                if [[ "$CONFIG_PING" != "$CURRENT_PING" ]]; then
                    continue
                fi
            fi

            # If all checks pass
            MATCH_FOUND=true
            break
        done

        if [[ "$MATCH_FOUND" = false ]]; then
            echo "No match found for:"
            echo "  Vendor: $CONFIG_VENDOR"
            echo "  Product: $CONFIG_PRODUCT"
            echo "  Speed: $CONFIG_SPEED"
            echo "  Port: $CONFIG_PORT"
            echo "  Ping: $CONFIG_PING"
            exit 1
        fi
    done <<< "$(echo "$CONFIG_INTERFACES")"

    echo "All network interfaces match the configuration."
}

# Main script logic

# Check for required utilities
check_dependencies

# Initialize variables
EXPECTED_COUNT=0
CONFIG_FILE="net_config.json"  # Default configuration file
SET_OPTIONS=""
CREATE_CONFIG=false
CHECK_CONFIG=false

# Parse arguments
while getopts ":i:c:s:" opt; do
    case $opt in
        i)
            EXPECTED_COUNT=$OPTARG
            ;;
        c)
            CONFIG_FILE=$OPTARG
            CHECK_CONFIG=true
            ;;
        s)
            SET_OPTIONS=$OPTARG
            CREATE_CONFIG=true
            ;;
        *)
            usage
            ;;
    esac
done

# Check for conflicting options
if $CREATE_CONFIG && $CHECK_CONFIG; then
    echo "Options -s and -c cannot be used simultaneously."
    usage
fi

# Create configuration mode
if $CREATE_CONFIG; then
    create_config
    exit 0
fi

# Verify configuration mode
if $CHECK_CONFIG; then
    check_config
    exit 0
fi

# If no mode is selected, display usage
usage
