# Makefile с явным указанием путей к библиотекам
CC      = gcc
OBJCOPY = objcopy

# Определяем архитектуру
ARCH    = $(shell uname -m)

# Пути к заголовочным файлам
EFIINC  = /usr/include/efi
INCLUDE = -I$(EFIINC) -I$(EFIINC)/$(ARCH) -I$(EFIINC)/protocol

# Специфичные для компилятора флаги
CFLAGS  = $(INCLUDE) -fno-stack-protector -fpic -fshort-wchar -mno-red-zone -Wall
ifeq ($(ARCH),x86_64)
    CFLAGS += -DEFI_FUNCTION_WRAPPER
endif

# Находим пути к библиотекам
EFI_LIB_DIR = $(shell find /usr/lib* -name "libefi.a" -o -name "libgnuefi.a" | head -n 1 | xargs dirname)
EFI_LIBS    = $(EFI_LIB_DIR)/libefi.a $(EFI_LIB_DIR)/libgnuefi.a

# Находим LDS файл
EFI_LDS_PATH = $(shell find /usr/lib* -name "elf_$(ARCH)_efi.lds" | head -n 1)

# Находим стартовый объектный файл
EFI_CRT0_PATH = $(shell find /usr/lib* -name "crt0-efi-$(ARCH).o" | head -n 1)

# Путь к libgcc
LIBGCC = $(shell $(CC) -print-libgcc-file-name)

# Флаги для линкера
LDFLAGS = -nostdlib -znocombreloc -shared -Bsymbolic 

TARGET  = minimal

all: $(TARGET).efi

# Показать найденные пути
paths:
	@echo "EFI_LIB_DIR: $(EFI_LIB_DIR)"
	@echo "EFI_LIBS: $(EFI_LIBS)"
	@echo "EFI_LDS_PATH: $(EFI_LDS_PATH)"
	@echo "EFI_CRT0_PATH: $(EFI_CRT0_PATH)"
	@echo "LIBGCC: $(LIBGCC)"

$(TARGET).o: $(TARGET).c
	$(CC) $(CFLAGS) -c $(TARGET).c -o $(TARGET).o

# Прямое указание библиотек вместо -l флагов
$(TARGET).so: $(TARGET).o
	ld $(LDFLAGS) -T $(EFI_LDS_PATH) $(EFI_CRT0_PATH) $(TARGET).o -o $@ $(EFI_LIBS) $(LIBGCC)

$(TARGET).efi: $(TARGET).so
	$(OBJCOPY) -j .text -j .sdata -j .data -j .dynamic \
           -j .dynsym -j .rel -j .rela -j .reloc \
           --target=efi-app-$(ARCH) $< $@

clean:
	rm -f $(TARGET).o $(TARGET).so $(TARGET).efi