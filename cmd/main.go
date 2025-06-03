package main

import (
	"fmt"
	"io"
	"log"
	"main/internal/config"
	coreconfig "main/tools/pkg/core_config"
	"net/http"
	"os"
	"os/exec"
	"path/filepath" // Для работы с путями и расширениями

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func recognizeSpeech(audioFilePath string, targetLang string) (string, error) {
	log.Printf("STT: Processing %s for lang %s", audioFilePath, targetLang)
	// Здесь будет реальная логика обращения к STT API
	// Убедитесь, что STT сервис может справиться с параллельными запросами
	// или реализуйте очередь/ограничитель, если это необходимо.
	if _, err := os.Stat(audioFilePath); os.IsNotExist(err) {
		return "", fmt.Errorf("audio file not found for STT: %s", audioFilePath)
	}

	// Искусственная задержка для имитации работы STT
	// time.Sleep(2 * time.Second)
	return "Распознанный текст для файла: " + filepath.Base(audioFilePath), nil
}

func convertOgaToWav(ogaPath string, wavPath string) error {
	// ffmpeg -i input.oga -acodec pcm_s16le -ar 16000 -ac 1 output.wav
	cmd := exec.Command("ffmpeg", "-i", ogaPath, "-y", "-acodec", "pcm_s16le", "-ar", "16000", "-ac", "1", wavPath)
	output, err := cmd.CombinedOutput() // Получаем и stdout, и stderr
	if err != nil {
		log.Printf("ffmpeg error for %s -> %s: %v\nOutput: %s", ogaPath, wavPath, err, string(output))
		return fmt.Errorf("ffmpeg conversion failed: %w. Output: %s", err, string(output))
	}
	log.Printf("Converted %s to %s", ogaPath, wavPath)
	return nil
}

func downloadFile(bot *tgbotapi.BotAPI, fileID string, localPath string) error {
	fileConfig := tgbotapi.FileConfig{FileID: fileID}
	file, err := bot.GetFile(fileConfig)
	if err != nil {
		return fmt.Errorf("bot.GetFile failed: %w", err)
	}

	url := file.Link(bot.Token)       // Используйте bot.Token, если он не пустой
	if bot.Token == "" && url == "" { // Для некоторых библиотек/конфигураций ссылка может быть уже полной
		url = file.FilePath // Предполагаем, что FilePath содержит полный URL, если токен не нужен для формирования ссылки
	} else if url == "" && file.FilePath != "" { // Если Link пуст, но FilePath есть
		url = fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", bot.Token, file.FilePath)
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("http.Get failed for %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bad status: %s, body: %s", resp.Status, string(bodyBytes))
	}

	out, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("os.Create failed for %s: %w", localPath, err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("io.Copy failed: %w", err)
	}
	log.Printf("Downloaded file %s to %s", fileID, localPath)
	return nil
}

func handleVoiceMessage(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	voice := message.Voice
	chatID := message.Chat.ID

	log.Printf("[%s] (ChatID: %d) sent a voice message (FileID: %s, Duration: %d)",
		message.From.UserName, chatID, voice.FileID, voice.Duration)

	// 1. Создаем временный файл для .oga
	ogaTempFile, err := os.CreateTemp("", "voice-*.oga")
	if err != nil {
		log.Printf("Error creating temp oga file: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка сервера: не удалось создать временный файл для аудио."))
		return
	}
	ogaFilePath := ogaTempFile.Name()
	ogaTempFile.Close() // Закрываем сразу, так как downloadFile и ffmpeg будут открывать его сами
	defer func() {
		log.Printf("Attempting to remove oga file: %s", ogaFilePath)
		if err := os.Remove(ogaFilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("Error removing temp oga file %s: %v", ogaFilePath, err)
		}
	}()

	// 2. Скачать .oga файл
	err = downloadFile(bot, voice.FileID, ogaFilePath)
	if err != nil {
		log.Printf("Error downloading voice file (ID: %s): %v", voice.FileID, err)
		bot.Send(tgbotapi.NewMessage(chatID, "Не удалось скачать голосовое сообщение."))
		return
	}

	// 3. Создаем временный файл для .wav
	wavTempFile, err := os.CreateTemp("", "voice-*.wav")
	if err != nil {
		log.Printf("Error creating temp wav file: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка сервера: не удалось создать временный файл для конвертации."))
		return
	}
	wavFilePath := wavTempFile.Name()
	wavTempFile.Close() // Закрываем сразу
	defer func() {
		log.Printf("Attempting to remove wav file: %s", wavFilePath)
		if err := os.Remove(wavFilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("Error removing temp wav file %s: %v", wavFilePath, err)
		}
	}()

	// 4. Конвертировать в WAV
	err = convertOgaToWav(ogaFilePath, wavFilePath)
	if err != nil {
		log.Printf("Error converting audio from %s to %s: %v", ogaFilePath, wavFilePath, err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка конвертации аудио."))
		return
	}

	// 5. Отправить в STT сервис
	recognizedText, err := recognizeSpeech(wavFilePath, "ru-RU")
	if err != nil {
		log.Printf("Error recognizing speech for file %s: %v", wavFilePath, err)
		bot.Send(tgbotapi.NewMessage(chatID, "Не удалось распознать речь."))
		return
	}

	// 6. Отправить результат пользователю
	msg := tgbotapi.NewMessage(chatID, recognizedText)
	msg.ReplyToMessageID = message.MessageID
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Error sending message to chat %d: %v", chatID, err)
	}
}

func main() {
	cfg := &config.Config{}
	// инициализация конфига
	if err := coreconfig.Load(cfg, ""); err != nil {
		log.Panic("Can't load config file", err)
	}

	botToken := cfg.TelegramBotToken
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable not set")
	}

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true // Установите в false для продакшена
	log.Printf("Authorized on account %s", bot.Self.UserName)

	// Создаем директорию для временных файлов, если ее нет (os.CreateTemp может использовать системную)
	// Но если вы хотите свою, то:
	// tempDir := "./temp_audio"
	// if _, err := os.Stat(tempDir); os.IsNotExist(err) {
	// 	os.Mkdir(tempDir, 0755)
	// }

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	// Ограничение количества одновременно обрабатываемых запросов (опционально)
	// concurrencyLimit := 10 // Например, не более 10 одновременных обработок
	// semaphore := make(chan struct{}, concurrencyLimit)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		// Обрабатываем каждое сообщение в отдельной горутине
		// чтобы не блокировать получение следующих обновлений
		go func(currentUpdate tgbotapi.Update) {
			message := currentUpdate.Message
			if message.Voice != nil {
				// Пример с семафором для ограничения параллельных задач
				// semaphore <- struct{}{} // Занимаем слот
				// defer func() { <-semaphore }() // Освобождаем слот

				handleVoiceMessage(bot, message)

			} else if message.Text != "" && message.Text == "/start" {
				msg := tgbotapi.NewMessage(message.Chat.ID, "Привет! Отправь мне голосовое сообщение, и я попробую его распознать.")
				bot.Send(msg)
			} else if message.Text != "" {
				msg := tgbotapi.NewMessage(message.Chat.ID, "Я понимаю только голосовые сообщения или команду /start.")
				bot.Send(msg)
			}
		}(update)
	}
}
