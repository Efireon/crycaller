#!/bin/bash
# RAM Test Script for Linux
# This script checks the total amount of RAM, verifies the number of RAM modules,
# displays the manufacturer(s) of the RAM, generates a JSON file with RAM information
# (with any excluded fields replaced by "*" if requested), and verifies RAM configuration
# against a configuration file.
#
# Both the "locator" and "bank_locator" fields are saved.
# When generating JSON with -s "slot", the locator and bank_locator fields are output as "*".
# In the configuration check, expected values of "*" mean that any value is acceptable.
#
# Example configuration (ram_config.json):
# {
#     "RAM": {
#         "modules": [
#             {
#                 "locator": "*",
#                 "bank_locator": "*",
#                 "manufacturer": "*",
#                 "volume": "*",
#                 "speed": "*"
#             },
#             {
#                 "locator": "DIMM 1",
#                 "bank_locator": "P0 CHANNEL B",
#                 "manufacturer": "Samsung",
#                 "volume": "8 GB",
#                 "speed": "3200 MT/s"
#             }
#         ]
#     }
# }

set -euo pipefail
IFS=$'\n\t'

usage() {
    echo "Usage: $0 [-s fields_to_exclude] [-c [config_path]] [-h]"
    echo "  -s fields_to_exclude  Specify fields to exclude from JSON (comma-separated, e.g., slot,manufacturer,count,volume,speed)"
    echo "  -c [config_path]      Check RAM configuration against a config file (default: ram_config.json)"
    echo "  -h                    Display this help message"
    exit 1
}

check_dependencies() {
    local dependencies=("dmidecode" "jq")
    for cmd in "${dependencies[@]}"; do
        if ! command -v "$cmd" &> /dev/null; then
            echo "Error: $cmd is not installed. Please install it and try again."
            exit 1
        fi
    done
}

generate_json=false
exclude_fields=()
check_config=false
config_path="ram_config.json"

while getopts "s:c:h" opt; do
    case "$opt" in
        s)
            generate_json=true
            IFS=',' read -r -a exclude_fields <<< "$(echo "$OPTARG" | tr -d ' ')"
            ;;
        c)
            check_config=true
            if [[ -n "$OPTARG" && ! "$OPTARG" =~ ^- ]]; then
                config_path="$OPTARG"
            else
                config_path="ram_config.json"
                OPTIND=$((OPTIND - 1))
            fi
            ;;
        h|*)
            usage
            ;;
    esac
done

if [[ "$EUID" -ne 0 ]]; then
    echo "This script requires root privileges. Please run it with sudo or as root."
    exit 1
fi

check_dependencies

echo "Starting RAM test..."

# Get total memory
total_ram_kb=$(grep MemTotal /proc/meminfo | awk '{print $2}')
total_ram_gb=$(awk "BEGIN {printf \"%.2f\", $total_ram_kb/1048576}")
echo "Total RAM: $total_ram_gb GB"

# Get total number of memory slots from Physical Memory Array
physical_memory_array=$(dmidecode --type memory | grep -A20 "Physical Memory Array")
number_of_devices=$(echo "$physical_memory_array" | grep "Number Of Devices:" | awk '{print $4}')
if [[ -z "$number_of_devices" ]]; then
    echo "Error: Could not determine the number of memory slots."
    exit 1
fi
echo "Total memory slots (from Physical Memory Array): $number_of_devices"

# Gather information about each memory device
declare -A ram_modules
declare -a all_slots
module_count=0
device=0

while IFS= read -r line; do
    if [[ "$line" == "Memory Device" ]]; then
        device=1
        declare -A current_module
    elif [[ "$device" -eq 1 ]]; then
        if [[ "$line" =~ ^[[:space:]]*Size:[[:space:]]*(.*) ]]; then
            size="${BASH_REMATCH[1]}"
            current_module["size"]="$size"
        elif [[ "$line" =~ ^[[:space:]]*Manufacturer:[[:space:]]*(.*) ]]; then
            manufacturer="${BASH_REMATCH[1]}"
            current_module["manufacturer"]="$manufacturer"
        elif [[ "$line" =~ ^[[:space:]]*Locator:[[:space:]]*(.*) ]]; then
            locator="${BASH_REMATCH[1]}"
            current_module["locator"]="$locator"
        elif [[ "$line" =~ ^[[:space:]]*Bank\ Locator:[[:space:]]*(.*) ]]; then
            bank_locator="${BASH_REMATCH[1]}"
            current_module["bank_locator"]="$bank_locator"
            # Save both; use locator for matching.
            current_module["slot"]="${current_module["locator"]}"
        elif [[ "$line" =~ ^[[:space:]]*(Speed|Configured\ Memory\ Speed):[[:space:]]*(.*) ]]; then
            speed="${BASH_REMATCH[2]}"
            current_module["speed"]="$speed"
        elif [[ "$line" =~ ^$ ]]; then
            if [[ "$size" != "No Module Installed" && -n "$size" ]]; then
                ram_modules["$module_count,size"]="$size"
                ram_modules["$module_count,manufacturer"]="${manufacturer:-Unknown}"
                ram_modules["$module_count,locator"]="${current_module["locator"]:-Unknown}"
                ram_modules["$module_count,bank_locator"]="${current_module["bank_locator"]:-Unknown}"
                ram_modules["$module_count,slot"]="${current_module["locator"]:-Unknown}"
                ram_modules["$module_count,speed"]="${current_module["speed"]:-Unknown}"
                all_slots+=("${current_module["locator"]}")
                module_count=$((module_count + 1))
            else
                all_slots+=("${current_module["locator"]}")
            fi
            device=0
        fi
    fi
done < <(dmidecode --type memory)

echo "Detected RAM modules: $module_count"

# Configuration check, if specified
if [[ "$check_config" == true ]]; then
    if [[ ! -f "$config_path" ]]; then
        echo "Error: Config file '$config_path' does not exist."
        exit 1
    fi

    config_modules=$(jq -c '.RAM.modules[]' "$config_path")
    declare -A expected_modules
    for module in $config_modules; do
        expected_locator=$(echo "$module" | jq -r '.locator')
        expected_bank_locator=$(echo "$module" | jq -r '.bank_locator')
        expected_manufacturer=$(echo "$module" | jq -r '.manufacturer')
        expected_volume=$(echo "$module" | jq -r '.volume')
        expected_speed=$(echo "$module" | jq -r '.speed')
        expected_modules["$expected_locator,$expected_bank_locator,manufacturer"]="$expected_manufacturer"
        expected_modules["$expected_locator,$expected_bank_locator,volume"]="$expected_volume"
        expected_modules["$expected_locator,$expected_bank_locator,speed"]="$expected_speed"
    done

    expected_count=$(jq '.RAM.modules | length' "$config_path")
    mismatch=false
    if [[ "$module_count" -ne "$expected_count" ]]; then
        printf "Module count verification: FAILED (Expected: %d, Found: %d)\n" "$expected_count" "$module_count"
        mismatch=true
    else
        echo "Module count verification: PASSED"
    fi

    for module in $config_modules; do
        expected_locator=$(echo "$module" | jq -r '.locator')
        expected_bank_locator=$(echo "$module" | jq -r '.bank_locator')
        expected_manufacturer=$(echo "$module" | jq -r '.manufacturer')
        expected_volume=$(echo "$module" | jq -r '.volume')
        expected_speed=$(echo "$module" | jq -r '.speed')
        found=false
        for ((i=0; i<module_count; i++)); do
            current_locator="${ram_modules["$i,locator"]}"
            current_bank_locator="${ram_modules["$i,bank_locator"]}"
            if [[ ("$expected_locator" == "*" || "$current_locator" == "$expected_locator") && ("$expected_bank_locator" == "*" || "$current_bank_locator" == "$expected_bank_locator") ]]; then
                found=true
                actual_manufacturer="${ram_modules["$i,manufacturer"]}"
                actual_volume="${ram_modules["$i,size"]}"
                actual_speed="${ram_modules["$i,speed"]:-Unknown}"
                if [[ "$expected_manufacturer" != "*" ]]; then
                    if [[ "$actual_manufacturer" != "$expected_manufacturer" ]]; then
                        printf "Mismatch in module at locator '%s' bank_locator '%s' manufacturer: Expected '%s', Found '%s'\n" "$expected_locator" "$expected_bank_locator" "$expected_manufacturer" "$actual_manufacturer"
                        mismatch=true
                    else
                        printf "Module at locator '%s' bank_locator '%s' manufacturer matches: '%s'\n" "$expected_locator" "$expected_bank_locator" "$actual_manufacturer"
                    fi
                else
                    printf "Module at locator '%s' bank_locator '%s' manufacturer check skipped (wildcard '*')\n" "$expected_locator" "$expected_bank_locator"
                fi
                if [[ "$expected_volume" != "*" ]]; then
                    if [[ "$actual_volume" != "$expected_volume" ]]; then
                        printf "Mismatch in module at locator '%s' bank_locator '%s' volume: Expected '%s', Found '%s'\n" "$expected_locator" "$expected_bank_locator" "$expected_volume" "$actual_volume"
                        mismatch=true
                    else
                        printf "Module at locator '%s' bank_locator '%s' volume matches: '%s'\n" "$expected_locator" "$expected_bank_locator" "$actual_volume"
                    fi
                else
                    printf "Module at locator '%s' bank_locator '%s' volume check skipped (wildcard '*')\n" "$expected_locator" "$expected_bank_locator"
                fi
                if [[ "$expected_speed" != "*" ]]; then
                    if [[ "$actual_speed" != "$expected_speed" ]]; then
                        printf "Mismatch in module at locator '%s' bank_locator '%s' speed: Expected '%s', Found '%s'\n" "$expected_locator" "$expected_bank_locator" "$expected_speed" "$actual_speed"
                        mismatch=true
                    else
                        printf "Module at locator '%s' bank_locator '%s' speed matches: '%s'\n" "$expected_locator" "$expected_bank_locator" "$actual_speed"
                    fi
                else
                    printf "Module at locator '%s' bank_locator '%s' speed check skipped (wildcard '*')\n" "$expected_locator" "$expected_bank_locator"
                fi
            fi
        done
        if [[ "$found" == false ]]; then
            printf "Module expected at locator '%s' bank_locator '%s' was not found.\n" "$expected_locator" "$expected_bank_locator"
            mismatch=true
        fi
    done

    if [[ "$mismatch" == false ]]; then
        echo "RAM configuration matches the configuration file."
    else
        echo "RAM configuration does not match the configuration file."
        exit 1
    fi
fi

# Display RAM manufacturers (if not checking configuration)
if [[ "$check_config" != true ]]; then
    manufacturers=()
    for ((i=0; i<module_count; i++)); do
        manufacturers+=("${ram_modules["$i,manufacturer"]}")
    done
    unique_manufacturers=($(echo "${manufacturers[@]}" | tr ' ' '\n' | sort -u | tr '\n' ' '))
    echo "RAM Manufacturer(s): ${unique_manufacturers[*]:-Unknown}"
fi

# Display occupied RAM slots (if not checking configuration)
if [[ "$check_config" != true ]]; then
    echo "Occupied RAM slots:"
    occupied_slots=()
    for ((i=0; i<module_count; i++)); do
        slot="${ram_modules["$i,locator"]}"
        bank="${ram_modules["$i,bank_locator"]}"
        echo "  Locator: $slot, Bank Locator: $bank"
        occupied_slots+=("$slot")
    done
fi

# Generate JSON if specified (empty slots are not included)
if $generate_json; then
    echo "Generating JSON file..."
    json_output="{ \"RAM\": {"
    if [[ ! " ${exclude_fields[*]} " =~ "count" ]]; then
        json_output+="\"count\": $module_count,"
    fi
    json_output+="\"modules\": ["
    for ((i=0; i<module_count; i++)); do
        json_output+="{"
        fields=()
        if [[ " ${exclude_fields[*]} " =~ "slot" ]]; then
            fields+=("\"locator\": \"*\"")
            fields+=("\"bank_locator\": \"*\"")
        else
            fields+=("\"locator\": \"${ram_modules["$i,locator"]:-Unknown}\"")
            fields+=("\"bank_locator\": \"${ram_modules["$i,bank_locator"]:-Unknown}\"")
        fi
        if [[ " ${exclude_fields[*]} " =~ "manufacturer" ]]; then
            fields+=("\"manufacturer\": \"*\"")
        else
            fields+=("\"manufacturer\": \"${ram_modules["$i,manufacturer"]}\"")
        fi
        if [[ " ${exclude_fields[*]} " =~ "volume" ]]; then
            fields+=("\"volume\": \"*\"")
        else
            fields+=("\"volume\": \"${ram_modules["$i,size"]}\"")
        fi
        if [[ " ${exclude_fields[*]} " =~ "speed" ]]; then
            fields+=("\"speed\": \"*\"")
        else
            fields+=("\"speed\": \"${ram_modules["$i,speed"]:-Unknown}\"")
        fi
        json_output+=$(IFS=,; echo "${fields[*]}")
        json_output+="}"
        if [[ $i -lt $((module_count - 1)) ]]; then
            json_output+=", "
        fi
    done
    json_output+="] } }"
    echo "$json_output" | jq . > ram_info.json
    echo "JSON file generated at ram_info.json"
fi

exit 0
