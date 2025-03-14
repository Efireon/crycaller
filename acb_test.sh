#!/bin/bash

###############################################################################
# SETTINGS
###############################################################################

# Log file in the current directory
LOG_FILE="$(pwd)/battery_monitor.log"

# PID file in /tmp to prevent multiple instances
PID_FILE="/tmp/battery_monitor.pid"

# Testing parameters
REQUIRED_CONFIRMATIONS=2       # Number of confirmations required to pass the test
MAX_INCORRECT_CHANGES=3        # Maximum number of incorrect changes before failing the test
MISBEHAVIOR_DURATION=18        # Duration of misbehavior in seconds

# Flags
QUIET_MODE=false

###############################################################################
# FUNCTIONS
###############################################################################

# Function to get the current battery level in percentage
get_battery_level() {
    cat /sys/class/power_supply/BAT0/capacity 2>/dev/null || echo "Unknown"
}

# Function to get the current battery charge in µAh
get_battery_charge() {
    cat /sys/class/power_supply/BAT0/energy_now 2>/dev/null || cat /sys/class/power_supply/BAT0/charge_now 2>/dev/null || echo "0"
}

# Function to get the current battery status
get_battery_status() {
    cat /sys/class/power_supply/BAT0/status 2>/dev/null || echo "Unknown"
}

# Function to log messages
log_message() {
    local message="$1"
    # Always write to the log file
    echo "$(date '+%Y-%m-%d %H:%M:%S') - $message" >> "$LOG_FILE"

    # Output to the console only if not in quiet mode
    if [ "$QUIET_MODE" = false ]; then
        echo "$(date '+%Y-%m-%d %H:%M:%S') - $message"
    fi
}

# Function to check if the script is already running
check_existing_instance() {
    if [ -f "$PID_FILE" ]; then
        existing_pid=$(cat "$PID_FILE")
        if ps -p "$existing_pid" > /dev/null 2>&1; then
            log_message "Another instance of this script is already running with PID $existing_pid."
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

# Parse command-line arguments
while [[ "$#" -gt 0 ]]; do
    case $1 in
        -q)
            QUIET_MODE=true
            ;;
        *)
            echo "Unknown argument: $1"
            echo "Usage: $0 [-q]"
            exit 1
            ;;
    esac
    shift
done

###############################################################################
# INITIALIZATION MANAGEMENT
###############################################################################

# Check for existing instances
check_existing_instance

# If running in quiet mode, write the PID file
if [ "$QUIET_MODE" = true ]; then
    echo $$ > "$PID_FILE"
fi

###############################################################################
# MAIN LOGIC
###############################################################################

log_message "=== Starting Battery Monitoring ==="

# Variables for tracking test status

# Charging test
charging_correct_changes=0
charging_incorrect_changes=0
charging_last_correct_change_time=0
charging_test_passed=false

# Discharging test
discharging_correct_changes=0
discharging_incorrect_changes=0
discharging_last_correct_change_time=0
discharging_test_passed=false

# Previous charge state and mode
last_charge=$(get_battery_charge)
last_mode="Unknown"

# Handle signals for graceful termination
trap "log_message 'Monitoring terminated by user.'; rm -f \"$PID_FILE\"; exit 0" SIGINT SIGTERM

# Infinite monitoring loop
while true; do
    current_level=$(get_battery_level)
    current_charge=$(get_battery_charge)
    current_status=$(get_battery_status)
    current_time=$(date +%s)  # Current time in seconds

    # Validate retrieved data
    if [[ "$current_charge" == "Unknown" || "$current_charge" -eq 0 ]]; then
        log_message "Failed to retrieve current battery charge."
        sleep 1
        continue
    fi

    # Determine the current battery mode
    if [[ "$current_status" == "Charging" ]]; then
        current_mode="Charging"
    elif [[ "$current_status" == "Discharging" ]]; then
        current_mode="Discharging"
    else
        current_mode="Unknown"
    fi

    # Reset misbehavior timers when mode changes
    if [[ "$current_mode" != "$last_mode" ]]; then
        log_message "Battery mode changed from $last_mode to $current_mode. Resetting misbehavior timers."

        # Reset misbehavior timers only
        charging_last_correct_change_time=0
        discharging_last_correct_change_time=0

        # Update the last mode
        last_mode="$current_mode"
    fi

    if [[ "$current_mode" == "Charging" ]]; then
        log_message "Mode: Charging. Battery Level: $current_level%, Charge: $((current_charge / 1000)) mAh"

        if [[ "$current_charge" -gt "$last_charge" ]]; then

            # Reset misbehavior timers only
            charging_last_correct_change_time=0
            discharging_last_correct_change_time=0

            # Correct change
            charging_correct_changes=$((charging_correct_changes + 1))
            charging_incorrect_changes=0
            charging_last_correct_change_time=$current_time
            log_message "Charge is increasing. Confirmations: $charging_correct_changes/$REQUIRED_CONFIRMATIONS."

            if [[ "$charging_correct_changes" -ge "$REQUIRED_CONFIRMATIONS" && "$charging_test_passed" == false ]]; then
                log_message "Charging test successfully passed."
                charging_test_passed=true
            fi
        elif [[ "$current_charge" -lt "$last_charge" ]]; then
            # Incorrect change
            charging_incorrect_changes=$((charging_incorrect_changes + 1))
            charging_correct_changes=0
            log_message "Charge is decreasing during charging. Incorrect changes: $charging_incorrect_changes/$MAX_INCORRECT_CHANGES."

            if [[ "$charging_incorrect_changes" -ge "$MAX_INCORRECT_CHANGES" ]]; then
                log_message "Charging test failed: $MAX_INCORRECT_CHANGES incorrect changes in a row."
                rm -f "$PID_FILE"
                exit 1
            fi
        else
            # No change
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
            
            # Reset misbehavior timers only
            charging_last_correct_change_time=0
            discharging_last_correct_change_time=0

            # Correct change
            discharging_correct_changes=$((discharging_correct_changes + 1))
            discharging_incorrect_changes=0
            discharging_last_correct_change_time=$current_time
            log_message "Charge is decreasing. Confirmations: $discharging_correct_changes/$REQUIRED_CONFIRMATIONS."

            if [[ "$discharging_correct_changes" -ge "$REQUIRED_CONFIRMATIONS" && "$discharging_test_passed" == false ]]; then
                log_message "Discharging test successfully passed."
                discharging_test_passed=true
            fi
        elif [[ "$current_charge" -gt "$last_charge" ]]; then
            # Incorrect change
            discharging_incorrect_changes=$((discharging_incorrect_changes + 1))
            discharging_correct_changes=0
            log_message "Charge is increasing during discharging. Incorrect changes: $discharging_incorrect_changes/$MAX_INCORRECT_CHANGES."

            if [[ "$discharging_incorrect_changes" -ge "$MAX_INCORRECT_CHANGES" ]]; then
                log_message "Discharging test failed: $MAX_INCORRECT_CHANGES incorrect changes in a row."
                rm -f "$PID_FILE"
                exit 1
            fi
        else
            # No change
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
        log_message "Battery status: $current_status. No action required."
        # Reset misbehavior timers only
        charging_last_correct_change_time=0
        discharging_last_correct_change_time=0
    fi

    # Check if both tests have passed
    if [[ "$charging_test_passed" == true && "$discharging_test_passed" == true ]]; then
        log_message "Both tests successfully passed: Charging and discharging are functioning correctly."
        rm -f "$PID_FILE"
        exit 0
    fi

    # Update the last charge for the next loop iteration
    last_charge="$current_charge"

    # Pause to reduce CPU usage
    sleep 1
done
