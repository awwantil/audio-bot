package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"main/internal/config"
	coreconfig "main/tools/pkg/core_config"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath" // Для работы с путями и расширениями
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	concurrencyLimit  = 10
	bothubApiURL      = "https://bothub.chat/api/v2/openai/v1/audio/transcriptions"
	defaultAudioModel = "whisper-1" // Модель по умолчанию, как в вашем curl примере

	menuCommandRecognize = "🎤 Распознать речь"
	menuCommandInfo      = "ℹ️ Информация"
	menuCommandSettings  = "⚙️ Настройки"
)

// Структура для разбора JSON-ответа от API
type TranscriptionResponse struct {
	Text  string    `json:"text"` // Предполагаем, что текст находится в поле "text"
	Error *struct { // Опционально, для обработки ошибок от API
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error,omitempty"`
}

func recognizeSpeech(audioFilePath string, cfg *config.Config) (string, error) {
	log.Printf("STT: Processing %s with Bothub API", audioFilePath)

	// 1. Открыть аудиофайл
	file, err := os.Open(audioFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to open audio file %s: %w", audioFilePath, err)
	}
	defer file.Close()

	// 2. Создать тело multipart/form-data запроса
	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)

	// Добавить поле 'file'
	fileWriter, err := multipartWriter.CreateFormFile("file", filepath.Base(audioFilePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file for %s: %w", audioFilePath, err)
	}
	_, err = io.Copy(fileWriter, file)
	if err != nil {
		return "", fmt.Errorf("failed to copy file content to multipart writer: %w", err)
	}

	// Добавить поле 'model'
	err = multipartWriter.WriteField("model", defaultAudioModel)
	if err != nil {
		return "", fmt.Errorf("failed to write model field to multipart writer: %w", err)
	}

	// Завершить формирование multipart-тела
	// Это важно, так как записывает финальный boundary
	err = multipartWriter.Close()
	if err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// 3. Создать HTTP POST запрос
	req, err := http.NewRequest("POST", bothubApiURL, &requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to create new HTTP request: %w", err)
	}

	// 4. Установить заголовки
	req.Header.Set("Authorization", "Bearer "+cfg.BothubApiToken)
	// Content-Type устанавливается автоматически multipartWriter'ом, включая boundary
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	// 5. Выполнить запрос
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request to Bothub API: %w", err)
	}
	defer resp.Body.Close()

	// 6. Прочитать тело ответа
	responseBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body from Bothub API: %w", err)
	}

	// 7. Проверить статус-код ответа
	if resp.StatusCode != http.StatusOK {
		log.Printf("Bothub API returned non-OK status: %s. Response: %s", resp.Status, string(responseBodyBytes))
		// Попытаемся распарсить ошибку, если API ее возвращает в JSON
		var errorResp TranscriptionResponse
		if json.Unmarshal(responseBodyBytes, &errorResp) == nil && errorResp.Error != nil {
			return "", fmt.Errorf("Bothub API error: %s (Type: %s, Code: %s, Param: %s), HTTP Status: %s",
				errorResp.Error.Message, errorResp.Error.Type, errorResp.Error.Code, errorResp.Error.Param, resp.Status)
		}
		return "", fmt.Errorf("Bothub API request failed with status %s and body: %s", resp.Status, string(responseBodyBytes))
	}

	// 8. Распарсить JSON ответ
	var transcriptionResp TranscriptionResponse
	err = json.Unmarshal(responseBodyBytes, &transcriptionResp)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON response from Bothub API (%s): %w. Response body: %s", resp.Status, err, string(responseBodyBytes))
	}

	if transcriptionResp.Text == "" && transcriptionResp.Error == nil {
		// Это может случиться, если поле 'text' отсутствует или пустое, но ошибки нет
		log.Printf("Warning: Bothub API returned OK status but no text. Response: %s", string(responseBodyBytes))
		return "", fmt.Errorf("Bothub API returned no text in response. Response body: %s", string(responseBodyBytes))
	}
	if transcriptionResp.Error != nil {
		return "", fmt.Errorf("Bothub API returned an error in JSON response: %s (Type: %s)", transcriptionResp.Error.Message, transcriptionResp.Error.Type)
	}

	log.Printf("STT: Successfully recognized text: \"%s\"", transcriptionResp.Text)
	return transcriptionResp.Text, nil
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
	url := file.Link(bot.Token)
	if url == "" && file.FilePath != "" {
		url = fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", bot.Token, file.FilePath)
	}

	// Используем кастомный HTTP клиент с таймаутами
	client := &http.Client{
		Timeout: 30 * time.Second, // Общий таймаут на запрос
		Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second, // Таймаут на TLS handshake
			ResponseHeaderTimeout: 10 * time.Second, // Таймаут на получение заголовков ответа
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
	resp, err := client.Get(url)
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

func handleVoiceMessage(bot *tgbotapi.BotAPI, message *tgbotapi.Message, cfg *config.Config) {
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
	recognizedText, err := recognizeSpeech(wavFilePath, cfg)
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

func sendMainMenu(bot *tgbotapi.BotAPI, chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "Выберите опцию из меню или отправьте голосовое сообщение:")
	// Создаем клавиатуру
	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow( // Первый ряд кнопок
			tgbotapi.NewKeyboardButton(menuCommandRecognize),
			tgbotapi.NewKeyboardButton(menuCommandInfo),
		),
		tgbotapi.NewKeyboardButtonRow( // Второй ряд кнопок
			tgbotapi.NewKeyboardButton(menuCommandSettings),
		),
	)
	// keyboard.OneTimeKeyboard = true // Если нужно скрыть клавиатуру после одного нажатия
	keyboard.ResizeKeyboard = true // Делает кнопки более компактными

	msg.ReplyMarkup = keyboard
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Error sending main menu: %v", err)
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
		log.Fatal("NewBotAPI error: %v", err)
	}

	bot.Debug = true // Установите в false для продакшена
	log.Printf("Authorized on account %s", bot.Self.UserName)

	// Создаем директорию для временных файлов, если ее нет (os.CreateTemp может использовать системную)
	// tempDir := "./temp_audio"
	// if _, err := os.Stat(tempDir); os.IsNotExist(err) {
	// 	os.Mkdir(tempDir, 0755)
	// }

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	// Ограничение количества одновременно обрабатываемых запросов (опционально)
	semaphore := make(chan struct{}, concurrencyLimit)

	for update := range updates {
		// Обрабатываем только сообщения, пропускаем другие типы обновлений
		if update.Message == nil {
			continue
		}

		// Обрабатываем каждое сообщение в отдельной горутине, чтобы не блокировать получение других
		go func(currentUpdate tgbotapi.Update) {
			semaphore <- struct{}{}        // Занимаем слот
			defer func() { <-semaphore }() // Освобождаем слот

			message := currentUpdate.Message
			chatID := message.Chat.ID

			// Обработка команд и текстовых сообщений
			if message.IsCommand() {
				switch message.Command() {
				case "start", "menu":
					sendMainMenu(bot, chatID)
				default:
					msg := tgbotapi.NewMessage(chatID, "Неизвестная команда. Используйте /start или /menu для отображения меню.")
					bot.Send(msg)
				}
				return // Команда обработана, выходим из горутины для этого сообщения
			}

			// Обработка нажатий на кнопки ReplyKeyboard (они приходят как обычный текст)
			switch message.Text {
			case menuCommandRecognize:
				msg := tgbotapi.NewMessage(chatID, "Пожалуйста, отправьте мне голосовое сообщение для распознавания.")
				bot.Send(msg)
			case menuCommandInfo:
				msgText := "Я бот для распознавания речи.\n"
				msgText += "Отправьте мне голосовое сообщение, и я переведу его в текст.\n"
				msgText += "Используется API от bothub.chat (на базе OpenAI Whisper).\n"
				msgText += "Разработчик: Pomogalov Vladimir\n"
				msgText += "Версия: 0.1.0"
				msg := tgbotapi.NewMessage(chatID, msgText)
				bot.Send(msg)
			case menuCommandSettings:
				msg := tgbotapi.NewMessage(chatID, "Раздел настроек пока в разработке.")
				// Здесь можно добавить InlineKeyboardMarkup для настроек, если нужно
				bot.Send(msg)
			default:
				// Если это не команда и не кнопка меню, и не голосовое, то это просто текст
				if message.Voice == nil && message.Text != "" {
					// Можно предложить меню, если пользователь просто написал текст
					msg := tgbotapi.NewMessage(chatID, "Я не совсем понял. Может, выберете что-то из меню?")
					msg.ReplyToMessageID = message.MessageID
					bot.Send(msg)
					sendMainMenu(bot, chatID) // Или сразу показать меню
					log.Printf("[%s] sent text: %s", message.From.UserName, message.Text)
				}
			}

			// Обработка голосовых сообщений
			if message.Voice != nil {
				handleVoiceMessage(bot, message, cfg)
			}

		}(update)
	}
}
