package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/eiannone/keyboard"
	"github.com/gosuri/uilive"
)

const (
	sampleRate      = 44100 // Частота дискретизации (Гц)
	framesPerBuffer = 1024  // Количество сэмплов за один блок
	bytesPerSample  = 2     // 16-битный PCM: 2 байта на сэмпл
	sliderWidth     = 50    // Ширина слайдера (символов)
)

func main() {
	// Запускаем arecord для захвата аудио:
	// Параметры: 16-битный PCM (S16_LE), 44100 Гц, 1 канал (моно)
	cmd := exec.Command("arecord", "-f", "S16_LE", "-r", "44100", "-c", "1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatal("Error creating stdout pipe:", err)
	}
	if err := cmd.Start(); err != nil {
		log.Fatal("Error starting arecord:", err)
	}
	// Гарантируем завершение процесса arecord при выходе.
	defer cmd.Process.Kill()

	// Открываем клавиатуру для обработки ввода.
	if err := keyboard.Open(); err != nil {
		log.Fatal("Error opening keyboard:", err)
	}
	defer keyboard.Close()

	// Создаем uilive writer для динамического обновления вывода.
	writer := uilive.New()
	writer.Start()
	defer writer.Stop()

	// Выводим инструкции один раз.
	fmt.Fprintln(writer, "Microphone test. Speak into the microphone to see the audio level.")
	fmt.Fprintln(writer, "Press Y or Enter for PASS, or N or Esc for FAIL.")
	fmt.Fprintln(writer, "") // Пустая строка для слайдера

	// Канал для получения результата теста.
	resultChan := make(chan bool)

	// Горутина для обработки нажатия клавиш.
	go func() {
		for {
			char, key, err := keyboard.GetKey()
			if err != nil {
				log.Fatal("Keyboard error:", err)
			}
			if key == keyboard.KeyEsc || char == 'n' || char == 'N' {
				resultChan <- false
				return
			}
			if key == keyboard.KeyEnter || char == 'y' || char == 'Y' {
				resultChan <- true
				return
			}
		}
	}()

	// Буфер для аудиоданных.
	buf := make([]byte, framesPerBuffer*bytesPerSample)

	// Основной цикл обновления слайдера.
	for {
		// Считываем аудиоданные из arecord.
		n, err := stdout.Read(buf)
		if err != nil {
			log.Fatal("Error reading audio data:", err)
		}
		if n < len(buf) {
			continue
		}

		// Вычисляем RMS (среднеквадратичное значение) для оценки уровня сигнала.
		var sumSquares float64
		for i := 0; i < len(buf); i += bytesPerSample {
			sample := int16(binary.LittleEndian.Uint16(buf[i:]))
			s := float64(sample) / 32768.0 // нормализация в диапазон [-1, 1]
			sumSquares += s * s
		}
		rms := math.Sqrt(sumSquares / float64(framesPerBuffer))

		// Преобразуем RMS (от 0 до ~1) в процент (0–100).
		percent := int(rms * 100)
		if percent > 100 {
			percent = 100
		}

		// Формируем строку-слайдер, например: [#####     ]  45%
		filled := int(float64(percent) / 100 * sliderWidth)
		slider := fmt.Sprintf("[%s%s] %3d%%",
			strings.Repeat("#", filled),
			strings.Repeat(" ", sliderWidth-filled),
			percent)

		// Обновляем слайдер в выводе.
		// uilive обновляет вывод, перерисовывая все строки, которые переданы через writer.
		fmt.Fprintln(writer, slider)

		// Проверяем, нажата ли одна из клавиш.
		select {
		case res := <-resultChan:
			writer.Stop() // останавливаем uilive
			if res {
				fmt.Println("Test passed!")
				os.Exit(0)
			} else {
				fmt.Println("Test failed!")
				os.Exit(1)
			}
		default:
			// Продолжаем обновление.
		}

		time.Sleep(30 * time.Millisecond)
	}
}
