#!/bin/bash
###############################################################################
# SETTINGS
###############################################################################

# Log file in the current directory
LOG_FILE="$(pwd)/battery_monitor.log"

# PID file in /tmp to prevent multiple instances
PID_FILE="/tmp/battery_monitor.pid"

# Testing parameters for capacity mode ("cap")
REQUIRED_CONFIRMATIONS=3       # Number of confirmations required to pass the test
REQUIRED_PLUG_CONFIRMATIONS=1       # Number of confirmations required to pass the test
MAX_INCORRECT_CHANGES=6        # Maximum number of incorrect changes before failing the test
MISBEHAVIOR_DURATION=10        # Duration (in seconds) allowed without a correct change

# Additional testing parameter for plug mode (voltage drop test)
# Minimum voltage drop (in microvolts) required when unplugged (e.g., 100000 µV = 0.1 V)
VOLTAGE_DROP_THRESHOLD=100000

# Default test mode ("cap" for capacity, "plug" for voltage drop test)
TEST_MODE="cap"

# Flags
QUIET_MODE=false

###############################################################################
# FUNCTIONS
###############################################################################

# Get battery level (percentage)
get_battery_level() {
    cat /sys/class/power_supply/BAT0/capacity 2>/dev/null || echo "Unknown"
}

# Get battery charge (in µAh)
get_battery_charge() {
    cat /sys/class/power_supply/BAT0/energy_now 2>/dev/null || \
    cat /sys/class/power_supply/BAT0/charge_now 2>/dev/null || echo "0"
}

# Get battery voltage (in µV)
get_battery_voltage() {
    cat /sys/class/power_supply/BAT0/voltage_now 2>/dev/null || echo "0"
}

# Get battery status (Charging, Discharging, etc.)
get_battery_status() {
    cat /sys/class/power_supply/BAT0/status 2>/dev/null || echo "Unknown"
}

# Log messages to file and optionally to console
log_message() {
    local message="$1"
    echo "$(date '+%Y-%m-%d %H:%M:%S') - $message" >> "$LOG_FILE"
    if [ "$QUIET_MODE" = false ]; then
        echo "$(date '+%Y-%m-%d %H:%M:%S') - $message"
    fi
}

# Check if another instance is running
check_existing_instance() {
    if [ -f "$PID_FILE" ]; then
        existing_pid=$(cat "$PID_FILE")
        if ps -p "$existing_pid" > /dev/null 2>&1; then
            log_message "Another instance of the script is already running with PID $existing_pid."
            exit 1
        else
            log_message "Stale PID file found. Removing."
            rm -f "$PID_FILE"
        fi
    fi
}

###############################################################################
# ARGUMENT HANDLING
###############################################################################

# Parse command-line arguments.
# -q enables quiet mode.
# -m <mode> selects test mode: "cap" (default) or "plug".
while [[ "$#" -gt 0 ]]; do
    case $1 in
        -q)
            QUIET_MODE=true
            shift ;;
        -m)
            if [[ -n "$2" ]]; then
                TEST_MODE="$2"
                shift 2
            else
                echo "Missing mode for -m option."
                echo "Usage: $0 [-q] [-m <cap|plug>]"
                exit 1
            fi ;;
        *)
            echo "Unknown argument: $1"
            echo "Usage: $0 [-q] [-m <cap|plug>]"
            exit 1 ;;
    esac
done

###############################################################################
# INSTANCE INIT
###############################################################################

check_existing_instance
echo $$ > "$PID_FILE"

###############################################################################
# MAIN LOGIC
###############################################################################

log_message "=== Starting Battery Monitoring (Test mode: $TEST_MODE) ==="

# Variables common to both modes
last_mode="Unknown"

if [ "$TEST_MODE" = "cap" ]; then
    # Capacity Mode Variables
    charging_correct_changes=0
    charging_incorrect_changes=0
    charging_last_correct_change_time=0
    charging_test_passed=false

    discharging_correct_changes=0
    discharging_incorrect_changes=0
    discharging_last_correct_change_time=0
    discharging_test_passed=false

    last_charge=$(get_battery_charge)

    while true; do
        current_level=$(get_battery_level)
        current_charge=$(get_battery_charge)
        current_status=$(get_battery_status)
        current_time=$(date +%s)

        if [[ "$current_charge" == "Unknown" || "$current_charge" -eq 0 ]]; then
            log_message "Failed to retrieve current battery charge."
            sleep 1
            continue
        fi

        if [[ "$current_status" == "Charging" ]]; then
            current_mode="Charging"
        elif [[ "$current_status" == "Discharging" ]]; then
            current_mode="Discharging"
        else
            current_mode="Unknown"
        fi

        if [[ "$current_mode" != "$last_mode" ]]; then
            log_message "Battery mode changed from $last_mode to $current_mode. Resetting misbehavior timers."
            charging_last_correct_change_time=0
            discharging_last_correct_change_time=0
            last_mode="$current_mode"
        fi

        if [[ "$current_mode" == "Charging" ]]; then
            log_message "Mode: Charging. Battery Level: $current_level%, Charge: $((current_charge / 1000)) mAh"
            if [[ "$current_charge" -gt "$last_charge" ]]; then
                charging_correct_changes=$((charging_correct_changes + 1))
                charging_incorrect_changes=0
                charging_last_correct_change_time=$current_time
                log_message "Charge increasing. Confirmations: $charging_correct_changes/$REQUIRED_CONFIRMATIONS."
                if [[ "$charging_correct_changes" -ge "$REQUIRED_CONFIRMATIONS" && "$charging_test_passed" == false ]]; then
                    log_message "Charging test passed successfully."
                    charging_test_passed=true
                fi
            elif [[ "$current_charge" -lt "$last_charge" ]]; then
                charging_incorrect_changes=$((charging_incorrect_changes + 1))
                charging_correct_changes=0
                log_message "Charge decreasing while charging. Incorrect changes: $charging_incorrect_changes/$MAX_INCORRECT_CHANGES."
                if [[ "$charging_incorrect_changes" -ge "$MAX_INCORRECT_CHANGES" ]]; then
                    log_message "Charging test failed: $MAX_INCORRECT_CHANGES incorrect changes in a row."
                    rm -f "$PID_FILE"
                    exit 1
                fi
            else
                if (( charging_last_correct_change_time != 0 )); then
                    elapsed=$((current_time - charging_last_correct_change_time))
                    if [[ "$elapsed" -ge "$MISBEHAVIOR_DURATION" && "$charging_test_passed" == false ]]; then
                        log_message "Charging test failed: Charge did not increase for $MISBEHAVIOR_DURATION seconds."
                        rm -f "$PID_FILE"
                        exit 1
                    fi
                fi
            fi

        elif [[ "$current_mode" == "Discharging" ]]; then
            log_message "Mode: Discharging. Battery Level: $current_level%, Charge: $((current_charge / 1000)) mAh"
            if [[ "$current_charge" -lt "$last_charge" ]]; then
                discharging_correct_changes=$((discharging_correct_changes + 1))
                discharging_incorrect_changes=0
                discharging_last_correct_change_time=$current_time
                log_message "Charge decreasing. Confirmations: $discharging_correct_changes/$REQUIRED_CONFIRMATIONS."
                if [[ "$discharging_correct_changes" -ge "$REQUIRED_CONFIRMATIONS" && "$discharging_test_passed" == false ]]; then
                    log_message "Discharging test passed successfully."
                    discharging_test_passed=true
                fi
            elif [[ "$current_charge" -gt "$last_charge" ]]; then
                discharging_incorrect_changes=$((discharging_incorrect_changes + 1))
                discharging_correct_changes=0
                log_message "Charge increasing while discharging. Incorrect changes: $discharging_incorrect_changes/$MAX_INCORRECT_CHANGES."
                if [[ "$discharging_incorrect_changes" -ge "$MAX_INCORRECT_CHANGES" ]]; then
                    log_message "Discharging test failed: $MAX_INCORRECT_CHANGES incorrect changes in a row."
                    rm -f "$PID_FILE"
                    exit 1
                fi
            else
                if (( discharging_last_correct_change_time != 0 )); then
                    elapsed=$((current_time - discharging_last_correct_change_time))
                    if [[ "$elapsed" -ge "$MISBEHAVIOR_DURATION" && "$discharging_test_passed" == false ]]; then
                        log_message "Discharging test failed: Charge did not decrease for $MISBEHAVIOR_DURATION seconds."
                        rm -f "$PID_FILE"
                        exit 1
                    fi
                fi
            fi

        else
            log_message "Battery status: $current_status. No action taken."
            charging_last_correct_change_time=0
            discharging_last_correct_change_time=0
        fi

        if [[ "$charging_test_passed" == true && "$discharging_test_passed" == true ]]; then
            log_message "Both capacity tests passed successfully: Charging and discharging are functioning correctly."
            rm -f "$PID_FILE"
            exit 0
        fi

        last_charge="$current_charge"
        sleep 1
    done

elif [ "$TEST_MODE" = "plug" ]; then
    # Plug mode (voltage drop test)
    last_voltage=0
    plug_correct_changes=0
    plug_incorrect_changes=0
    plug_last_correct_change_time=0
    plug_test_passed=false

    while true; do
        current_status=$(get_battery_status)
        current_time=$(date +%s)
        current_voltage=$(get_battery_voltage)

        if [[ "$current_voltage" == "0" ]]; then
            log_message "Failed to retrieve battery voltage."
            sleep 1
            continue
        fi

        if [[ "$current_status" == "Charging" ]]; then
            current_mode="Charging"
            last_voltage=$(get_battery_voltage)
            log_message "Mode: Charging. Battery Voltage: $((last_voltage / 1000000)) V"
        elif [[ "$current_status" == "Discharging" ]]; then
            current_mode="Discharging"
            log_message "Mode: Discharging. Battery Voltage: $((current_voltage / 1000000)) V"
        else
            current_mode="Unknown"
            log_message "Battery status: $current_status. No voltage test action."
        fi

        if [[ "$current_mode" != "$last_mode" ]]; then
            log_message "Battery mode changed from $last_mode to $current_mode. Resetting misbehavior timers."
            plug_last_correct_change_time=0
            last_mode="$current_mode"
        fi

        if [[ "$current_mode" == "Discharging" ]]; then
            # If the voltage hasn't changed compared to the previous reading, skip this iteration.
            if [ "$current_voltage" -eq "$last_voltage" ]; then
                log_message "Voltage did not change (remains $((current_voltage / 1000000)) V). Skipping iteration."
                sleep 1
                continue
            fi

            if [ "$last_voltage" -gt 0 ]; then
                voltage_drop=$(( last_voltage - current_voltage ))
                if [[ "$voltage_drop" -ge "$VOLTAGE_DROP_THRESHOLD" ]]; then
                    plug_correct_changes=$((plug_correct_changes + 1))
                    plug_incorrect_changes=0
                    plug_last_correct_change_time=$current_time
                    log_message "Voltage drop confirmed: $voltage_drop µV. Confirmations: $plug_correct_changes/$REQUIRED_PLUG_CONFIRMATIONS."
                    if [[ "$plug_correct_changes" -ge "$REQUIRED_PLUG_CONFIRMATIONS" && "$plug_test_passed" == false ]]; then
                        log_message "Plug test passed successfully."
                        plug_test_passed=true
                    fi
                else
                    plug_incorrect_changes=$((plug_incorrect_changes + 1))
                    plug_correct_changes=0
                    log_message "Voltage drop insufficient: $voltage_drop µV. Incorrect count: $plug_incorrect_changes/$MAX_INCORRECT_CHANGES."
                    if [[ "$plug_incorrect_changes" -ge "$MAX_INCORRECT_CHANGES" ]]; then
                        log_message "Plug test failed: $MAX_INCORRECT_CHANGES insufficient voltage drops in a row."
                        rm -f "$PID_FILE"
                        exit 1
                    fi
                fi
            else
                log_message "No previous voltage recorded while charging."
            fi
        fi

        # In Charging mode, update last_voltage
        if [[ "$current_mode" == "Charging" ]]; then
            last_voltage=$(get_battery_voltage)
        fi

        if [[ "$plug_test_passed" == true ]]; then
            log_message "Plug test passed successfully: Voltage drop confirmed upon unplugging."
            rm -f "$PID_FILE"
            exit 0
        fi

        sleep 1
    done

else
    log_message "Unknown test mode: $TEST_MODE. Exiting."
    rm -f "$PID_FILE"
    exit 1
fi
