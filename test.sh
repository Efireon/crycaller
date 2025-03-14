#!/bin/bash

# Генерируем случайное трехзначное число с префиксом DRG
SERIAL_NUMBER="DRG$(printf "%03d" $(( RANDOM % 1000 )))"

# Имя UEFI-переменной и GUID (используй свой GUID!)
VAR_NAME="SerialNumber"
GUID="12345678-1234-1234-1234-123456789abc"

# Проверяем, что скрипт запущен от root
if [ "$EUID" -ne 0 ]; then
    echo "Ошибка: скрипт должен запускаться от root"
    exit 1
fi

# Атрибуты EFI переменной (стандартный набор: non-volatile, boot-service, runtime)
ATTRIBUTES="7"  # 7 - это (EFI_VARIABLE_NON_VOLATILE | EFI_VARIABLE_BOOTSERVICE_ACCESS | EFI_VARIABLE_RUNTIME_ACCESS)

# Создаём временный файл с сериалом
TMPFILE=$(mktemp)
echo -n "$SERIAL_NUMBER" > "$TMPFILE"cd

# Записываем EFI переменную через efivar
if efivar --write --name="${GUID}-${VAR_NAME}" --attributes=${ATTRIBUTES} --datafile="$TMPFILE"; then
    echo "Серийный номер $SERIAL_NUMBER успешно записан в EFI переменную."
else
    echo "Ошибка записи EFI переменной"
    rm -f "$TMPFILE"
    exit 1
fi

# Удаляем временный файл
rm -f "$TMPFILE"
