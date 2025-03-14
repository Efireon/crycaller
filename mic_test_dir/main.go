package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"sync"
	"time"
	"unsafe"

	"github.com/eiannone/keyboard"
)

/*
#cgo LDFLAGS: -lasound
#include <alsa/asoundlib.h>
*/
import "C"

// Тестовые параметры.
const (
	sampleRate = 44100 // Частота дискретизации (Hz)
	channels   = 1     // Моно
	chunkSize  = 1024  // Количество фреймов за вызов ALSA
	windowSize = 4096  // Размер скользящего окна (в сэмплах) для определения частоты
)

// Флаги командной строки:
// -t задаёт длительность теста (например, "3s")
// -f задаёт частоту тона (Hz)
// -y задаёт требуемое число подтверждений
// -q включает quiet mode – тест не спрашивает пользователя, а просто проваливается, если звук не обнаружен.
var (
	testDuration        = flag.Duration("t", 3*time.Second, "Test duration (e.g., 3s)")
	toneFrequency       = flag.Float64("f", 440.0, "Tone frequency (Hz)")
	confirmationsNeeded = flag.Int("y", 3, "Number of confirmations required for test pass")
	quiet               = flag.Bool("q", false, "Quiet mode: do not ask user confirmation, test fails if sound not detected")
)

// setMasterVolume100 устанавливает громкость канала "Master" на 100% с помощью amixer.
func setMasterVolume100() error {
	cmd := exec.Command("amixer", "sset", "Master", "100%")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set Master volume to 100%%: %v, output: %s", err, output)
	}
	return nil
}

// playTone воспроизводит синусоидальный тон через ALSA. Функция периодически проверяет контекст
// и завершает воспроизведение, если тест отменён.
func playTone(ctx context.Context, freq float64, dur time.Duration, rate int) error {
	var pcmHandle *C.snd_pcm_t
	devName := C.CString("default")
	defer C.free(unsafe.Pointer(devName))

	// Открываем устройство для воспроизведения.
	if errCode := C.snd_pcm_open(&pcmHandle, devName, C.SND_PCM_STREAM_PLAYBACK, 0); errCode < 0 {
		return fmt.Errorf("snd_pcm_open (playback) error: %s", C.GoString(C.snd_strerror(errCode)))
	}
	if errCode := C.snd_pcm_set_params(pcmHandle,
		C.SND_PCM_FORMAT_S16_LE,
		C.SND_PCM_ACCESS_RW_INTERLEAVED,
		channels,
		C.uint(rate),
		1,
		50000); errCode < 0 {
		C.snd_pcm_close(pcmHandle)
		return fmt.Errorf("snd_pcm_set_params (playback) error: %s", C.GoString(C.snd_strerror(errCode)))
	}

	totalFrames := rate * int(dur.Seconds())
	phase := 0.0
	phaseInc := 2 * math.Pi * freq / float64(rate)
	buf := make([]C.short, chunkSize)
	framesWritten := 0

	for framesWritten < totalFrames {
		// Если контекст отменён – выходим.
		select {
		case <-ctx.Done():
			C.snd_pcm_drain(pcmHandle)
			C.snd_pcm_close(pcmHandle)
			return nil
		default:
		}

		currentChunk := chunkSize
		if totalFrames-framesWritten < chunkSize {
			currentChunk = totalFrames - framesWritten
		}

		// Генерируем синусоидальные сэмплы.
		for i := 0; i < currentChunk; i++ {
			sample := math.Sin(phase)
			val := int16(sample * 32767)
			buf[i] = C.short(val)
			phase += phaseInc
			if phase >= 2*math.Pi {
				phase -= 2 * math.Pi
			}
		}

		var frames C.snd_pcm_sframes_t
		frames = C.snd_pcm_writei(pcmHandle, unsafe.Pointer(&buf[0]), C.snd_pcm_uframes_t(currentChunk))
		if frames < 0 {
			recovery := C.snd_pcm_recover(pcmHandle, C.int(frames), 0)
			if recovery < 0 {
				C.snd_pcm_close(pcmHandle)
				return fmt.Errorf("snd_pcm_writei error: %s", C.GoString(C.snd_strerror(C.int(recovery))))
			}
			frames = C.snd_pcm_sframes_t(recovery)
		}
		framesWritten += int(frames)
	}
	C.snd_pcm_drain(pcmHandle)
	C.snd_pcm_close(pcmHandle)
	return nil
}

// dynamicFrequencyMonitor считывает аудио с микрофона, обновляет скользящий буфер, вычисляет доминирующую частоту
// методом автокорреляции с параболической интерполяцией и подсчитывает подтверждения. При обнаружении частоты,
// удовлетворяющей условию (freq < 2000 и в пределах 5% от target), засчитывается подтверждение с выводом сообщения.
// Если число подтверждений достигает требуемого, вызывается cancel() и функция завершается.
func dynamicFrequencyMonitor(ctx context.Context, cancel context.CancelFunc, rate int, target float64, needed int) int {
	var pcmHandle *C.snd_pcm_t
	devName := C.CString("default")
	defer C.free(unsafe.Pointer(devName))

	if errCode := C.snd_pcm_open(&pcmHandle, devName, C.SND_PCM_STREAM_CAPTURE, 0); errCode < 0 {
		log.Fatalf("snd_pcm_open (capture) error: %s", C.GoString(C.snd_strerror(errCode)))
	}
	if errCode := C.snd_pcm_set_params(pcmHandle,
		C.SND_PCM_FORMAT_S16_LE,
		C.SND_PCM_ACCESS_RW_INTERLEAVED,
		channels,
		C.uint(rate),
		1,
		50000); errCode < 0 {
		C.snd_pcm_close(pcmHandle)
		log.Fatalf("snd_pcm_set_params (capture) error: %s", C.GoString(C.snd_strerror(errCode)))
	}

	windowBuffer := make([]int16, 0, windowSize)
	chunk := make([]C.short, chunkSize)
	// Предварительное заполнение окна.
	for len(windowBuffer) < windowSize {
		frames := C.snd_pcm_readi(pcmHandle, unsafe.Pointer(&chunk[0]), C.snd_pcm_uframes_t(chunkSize))
		if frames < 0 {
			recovery := C.snd_pcm_recover(pcmHandle, C.int(frames), 0)
			if recovery < 0 {
				C.snd_pcm_close(pcmHandle)
				log.Fatalf("snd_pcm_readi error: %s", C.GoString(C.snd_strerror(C.int(recovery))))
			}
			frames = C.snd_pcm_sframes_t(recovery)
		}
		for i := 0; i < int(frames) && len(windowBuffer) < windowSize; i++ {
			windowBuffer = append(windowBuffer, int16(chunk[i]))
		}
	}

	confirmations := 0
	tolerance := 0.05 * target

	for {
		select {
		case <-ctx.Done():
			C.snd_pcm_close(pcmHandle)
			return confirmations
		default:
		}

		frames := C.snd_pcm_readi(pcmHandle, unsafe.Pointer(&chunk[0]), C.snd_pcm_uframes_t(chunkSize))
		if frames < 0 {
			recovery := C.snd_pcm_recover(pcmHandle, C.int(frames), 0)
			if recovery < 0 {
				C.snd_pcm_close(pcmHandle)
				log.Fatalf("snd_pcm_readi error: %s", C.GoString(C.snd_strerror(C.int(recovery))))
			}
			frames = C.snd_pcm_sframes_t(recovery)
		}
		numFrames := int(frames)
		if numFrames > len(windowBuffer) {
			numFrames = len(windowBuffer)
		}
		windowBuffer = windowBuffer[numFrames:]
		for i := 0; i < int(frames); i++ {
			windowBuffer = append(windowBuffer, int16(chunk[i]))
		}
		if len(windowBuffer) > windowSize {
			windowBuffer = windowBuffer[len(windowBuffer)-windowSize:]
		}

		freq := detectFrequency(windowBuffer, rate)
		if freq < 2000 && math.Abs(freq-target) <= tolerance {
			confirmations++
			fmt.Printf("Confirmation %d collected (freq = %.2f Hz)\n", confirmations, freq)
			if confirmations >= needed {
				cancel()
				C.snd_pcm_close(pcmHandle)
				return confirmations
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// detectFrequency вычисляет доминирующую частоту в сэмплах методом автокорреляции с параболической интерполяцией.
func detectFrequency(samples []int16, rate int) float64 {
	N := len(samples)
	if N == 0 {
		return 0.0
	}
	fSamples := make([]float64, N)
	var sum float64
	for i, s := range samples {
		fSamples[i] = float64(s)
		sum += fSamples[i]
	}
	mean := sum / float64(N)
	for i := range fSamples {
		fSamples[i] -= mean
	}
	minLag := int(float64(rate) / 2000.0)
	if minLag < 1 {
		minLag = 1
	}
	maxLag := int(float64(rate) / 50.0)
	if maxLag > N/2 {
		maxLag = N / 2
	}
	autoCorr := make([]float64, maxLag+1)
	for lag := minLag; lag <= maxLag; lag++ {
		var s float64
		for i := 0; i < N-lag; i++ {
			s += fSamples[i] * fSamples[i+lag]
		}
		autoCorr[lag] = s
	}
	bestLag := minLag
	bestCorr := autoCorr[minLag]
	for lag := minLag + 1; lag <= maxLag; lag++ {
		if autoCorr[lag] > bestCorr {
			bestCorr = autoCorr[lag]
			bestLag = lag
		}
	}
	if bestLag <= minLag || bestLag >= maxLag {
		return float64(rate) / float64(bestLag)
	}
	rPrev := autoCorr[bestLag-1]
	r0 := autoCorr[bestLag]
	rNext := autoCorr[bestLag+1]
	denom := 2*r0 - rPrev - rNext
	delta := 0.0
	if denom != 0 {
		delta = 0.5 * (rPrev - rNext) / denom
	}
	interpLag := float64(bestLag) + delta
	return float64(rate) / interpLag
}

// waitForUserConfirmation ожидает нажатия клавиши и возвращает true, если пользователь подтвердил (Y/Enter),
// и false, если нажата клавиша N/Esc.
func waitForUserConfirmation() bool {
	if err := keyboard.Open(); err != nil {
		log.Fatal(err)
	}
	defer keyboard.Close()
	fmt.Println("Automatic confirmations were not reached.")
	fmt.Println("Did you hear the sound? (Y/Enter = Yes, N/Esc = No)")
	for {
		char, key, err := keyboard.GetKey()
		if err != nil {
			log.Fatal(err)
		}
		if char == 'y' || char == 'Y' || key == keyboard.KeyEnter {
			return true
		} else if char == 'n' || char == 'N' || key == keyboard.KeyEsc {
			return false
		}
	}
}

func main() {
	flag.Parse()

	// Устанавливаем громкость Master на 100%.
	if err := setMasterVolume100(); err != nil {
		log.Fatalf("Error setting volume: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Если тест длится дольше указанного времени, отменяем его по таймеру.
	timer := time.AfterFunc(*testDuration, cancel)
	defer timer.Stop()

	var wg sync.WaitGroup
	wg.Add(2)
	confirmCh := make(chan int, 1)

	// Горутина мониторинга частоты.
	go func() {
		defer wg.Done()
		confirms := dynamicFrequencyMonitor(ctx, cancel, sampleRate, *toneFrequency, *confirmationsNeeded)
		confirmCh <- confirms
	}()

	// Горутина воспроизведения тона.
	go func() {
		defer wg.Done()
		if err := playTone(ctx, *toneFrequency, *testDuration, sampleRate); err != nil {
			log.Fatalf("Playback error: %v", err)
		}
	}()

	wg.Wait()
	confirmations := <-confirmCh

	if confirmations >= *confirmationsNeeded {
		fmt.Println("Required confirmations collected. Test passed.")
		os.Exit(0)
	} else {
		if *quiet {
			fmt.Println("Quiet mode: required confirmations not reached. Test failed.")
			os.Exit(1)
		} else {
			if waitForUserConfirmation() {
				fmt.Println("User confirmed that sound was audible. Test passed.")
				os.Exit(0)
			} else {
				fmt.Println("User confirmed that sound was not audible. Test failed.")
				os.Exit(1)
			}
		}
	}
}
