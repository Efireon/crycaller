#include <efi.h>
#include <efilib.h>

// Строка с базовыми ASCII символами
static const CHAR16 HelloStr[] = {'H', 'e', 'l', 'l', 'o', ' ', 'U', 'E', 'F', 'I', '\r', '\n', 0};

EFI_STATUS
EFIAPI
efi_main(EFI_HANDLE ImageHandle, EFI_SYSTEM_TABLE *SystemTable)
{
    // Инициализация библиотеки
    InitializeLib(ImageHandle, SystemTable);
    
    // Сброс консоли
    SystemTable->ConOut->Reset(SystemTable->ConOut, FALSE);
    
    // Вывод отдельных символов вместо строки
    for(UINTN i = 0; HelloStr[i] != 0; i++) {
        CHAR16 c = HelloStr[i];
        CHAR16 buf[2] = {c, 0};  // Одиночный символ и завершающий 0
        SystemTable->ConOut->OutputString(SystemTable->ConOut, buf);
    }
    
    // Немедленный возврат
    return EFI_SUCCESS;
}