#!/bin/bash
set -e

# 1. Determine the boot device mounted at /run/archiso/bootmnt.
BOOT_MNT="/run/archiso/bootmnt"
BOOT_DEVICE=$(findmnt -n -o SOURCE "$BOOT_MNT")
if [ -z "$BOOT_DEVICE" ]; then
    echo "Error: Unable to determine the device mounted at $BOOT_MNT"
    exit 1
fi
echo "Device mounted at $BOOT_MNT: $BOOT_DEVICE"

# 2. Determine the base disk (e.g., from /dev/sda1 get /dev/sda).
if [[ $BOOT_DEVICE =~ ^(/dev/[a-z]+)([0-9]+)$ ]]; then
    BASE_DISK="${BASH_REMATCH[1]}"
elif [[ $BOOT_DEVICE =~ ^(/dev/nvme[0-9]+n[0-9]+)p([0-9]+)$ ]]; then
    BASE_DISK="${BASH_REMATCH[1]}"
else
    echo "Error: Unable to determine the base disk from $BOOT_DEVICE"
    exit 1
fi
echo "Base disk: $BASE_DISK"

# 2.1 Check if a partition with label PERSISTENT already exists.
PERSISTENT_PART=""
for part in $(lsblk -ln -o NAME,TYPE "$BASE_DISK" | awk '$2=="part" {print $1}'); do
    full="/dev/$part"
    label=$(blkid -s LABEL -o value "$full" 2>/dev/null || echo "")
    if [ "$label" == "PERSISTENT" ]; then
        PERSISTENT_PART="$full"
        break
    fi
done

if [ -n "$PERSISTENT_PART" ]; then
    echo "Persistent partition already exists: $PERSISTENT_PART"
    DESIRED_MOUNT="/root/progs"
    CURRENT_MOUNT=$(findmnt -n -o TARGET "$PERSISTENT_PART" || true)
    if [ "$CURRENT_MOUNT" == "$DESIRED_MOUNT" ]; then
        echo "Persistent partition $PERSISTENT_PART is already mounted at $DESIRED_MOUNT. No action needed."
        exit 0
    else
        if [ -n "$CURRENT_MOUNT" ]; then
            echo "Persistent partition $PERSISTENT_PART is mounted at $CURRENT_MOUNT. Remounting to $DESIRED_MOUNT..."
            umount "$PERSISTENT_PART"
        fi
        if [ ! -d "$DESIRED_MOUNT" ]; then
            mkdir -p "$DESIRED_MOUNT"
        fi
        mount "$PERSISTENT_PART" "$DESIRED_MOUNT"
        echo "Persistent partition $PERSISTENT_PART successfully mounted at $DESIRED_MOUNT."
        exit 0
    fi
fi

# 3. Calculate free space on the base disk using sfdisk.
# Get total sectors on the disk.
TOTAL_SECTORS=$(blockdev --getsz "$BASE_DISK")
if [ -z "$TOTAL_SECTORS" ]; then
    echo "Error: Unable to get total sectors for $BASE_DISK"
    exit 1
fi
echo "Total sectors on $BASE_DISK: $TOTAL_SECTORS"

# Get the end sector of the last partition.
LAST_END=$(sfdisk -l "$BASE_DISK" | awk -v disk="$BASE_DISK" '$1 ~ disk { if($3 > max) max=$3 } END { print max }')
if [ -z "$LAST_END" ]; then
    echo "Error: Unable to determine the end of the last partition on $BASE_DISK"
    exit 1
fi
echo "Last partition ends at sector: $LAST_END"

# New partition starts at LAST_END + 1.
NEW_START=$((LAST_END + 1))
if [ "$NEW_START" -ge "$TOTAL_SECTORS" ]; then
    echo "Error: No free space available on $BASE_DISK"
    exit 1
fi

# 4. Create a new partition from NEW_START to the end of the disk using sfdisk.
echo "Creating new partition from sector $NEW_START to end of disk..."
# Prepare input for sfdisk: "start,size,type". We leave size empty to use rest of disk.
echo "${NEW_START},," | sfdisk --force --append "$BASE_DISK"

# 5. Update partition table.
partprobe "$BASE_DISK"
sleep 2

# 6. Determine new partition name.
# We assume that the new partition has the highest partition number.
NEW_PART_NUM=$(sfdisk -l "$BASE_DISK" | awk -v disk="$BASE_DISK" '$1 ~ disk { if($1 ~ disk) { split($1,a,""); num=a[length(a)]; if(num+0 > max) max=num+0 } } END { print max }')
# Альтернативно, можно использовать lsblk:
NEW_PART_NUM=$(lsblk -ln -o NAME "$BASE_DISK" | grep -E "^$(basename $BASE_DISK)[0-9]+" | sed "s/$(basename $BASE_DISK)//" | sort -n | tail -n1)
if [ -z "$NEW_PART_NUM" ]; then
    echo "Error: Unable to determine new partition number on $BASE_DISK"
    exit 1
fi
if [[ $BASE_DISK =~ nvme ]]; then
    NEW_PARTITION="${BASE_DISK}p${NEW_PART_NUM}"
else
    NEW_PARTITION="${BASE_DISK}${NEW_PART_NUM}"
fi
echo "New partition created: $NEW_PARTITION"

# 7. Format the new partition as FAT32 with label PERSISTENT.
echo "Formatting $NEW_PARTITION as FAT32 with label PERSISTENT..."
mkfs.fat -F32 -n PERSISTENT "$NEW_PARTITION"

# 8. Mount the new partition to /mnt/persistent.
DESIRED_MOUNT="/root/progs"
if [ ! -d "$DESIRED_MOUNT" ]; then
    echo "Creating mount point: $DESIRED_MOUNT"
    mkdir -p "$DESIRED_MOUNT"
fi
echo "Mounting $NEW_PARTITION at $DESIRED_MOUNT..."
mount "$NEW_PARTITION" "$DESIRED_MOUNT"

if [ $? -eq 0 ]; then
    echo "Persistent partition $NEW_PARTITION successfully mounted at $DESIRED_MOUNT."
    exit 0
else
    echo "Error: Unable to mount $NEW_PARTITION at $DESIRED_MOUNT."
    exit 1
fi