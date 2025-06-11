package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"main/internal/config"
	"main/internal/model"
	coreconfig "main/tools/pkg/core_config"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	concurrencyLimit            = 10
	bothubApiURL                = "https://bothub.chat/api/v2/openai/v1/audio/transcriptions"
	bothubChatCompletionsApiURL = "https://bothub.chat/api/v2/openai/v1/chat/completions"
	defaultAudioModel           = "whisper-1"
	gptModelForYoutubeSummary   = "gpt-4o"
	maxMessageTextLength        = 4096

	menuCommandRecognize   = "🎤 Распознать речь"
	menuCommandInfo        = "ℹ️ Информация"
	menuCommandSettings    = "⚙️ Настройки"
	menuCommandYoutubeInfo = "🎞️ Инфо о Youtube-видео" // Новый пункт меню
)

var youtubeRegex = regexp.MustCompile(`^(https?://)?(www\.)?(youtube\.com/watch\?v=|youtu\.be/|youtube\.com/shorts/)[\w-]+(\S*)?$`)

func isValidYoutubeLink(url string) bool {
	return youtubeRegex.MatchString(url)
}

func recognizeSpeech(audioFilePath string, cfg *config.Config) (string, error) {
	log.Printf("STT: Processing %s with Bothub API", audioFilePath)

	file, err := os.Open(audioFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to open audio file %s: %w", audioFilePath, err)
	}
	defer file.Close()

	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)

	fileWriter, err := multipartWriter.CreateFormFile("file", filepath.Base(audioFilePath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file for %s: %w", audioFilePath, err)
	}
	_, err = io.Copy(fileWriter, file)
	if err != nil {
		return "", fmt.Errorf("failed to copy file content to multipart writer: %w", err)
	}

	err = multipartWriter.WriteField("model", defaultAudioModel)
	if err != nil {
		return "", fmt.Errorf("failed to write model field to multipart writer: %w", err)
	}

	err = multipartWriter.Close()
	if err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	req, err := http.NewRequest("POST", bothubApiURL, &requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to create new HTTP request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.BothubApiToken)
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second} // Увеличен таймаут для потенциально больших файлов
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request to Bothub API: %w", err)
	}
	defer resp.Body.Close()

	responseBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body from Bothub API: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("Bothub API returned non-OK status: %s. Response: %s", resp.Status, string(responseBodyBytes))
		var errorResp model.TranscriptionResponse
		if json.Unmarshal(responseBodyBytes, &errorResp) == nil && errorResp.Error != nil {
			return "", fmt.Errorf("Bothub API error: %s (Type: %s, Code: %s, Param: %s), HTTP Status: %s",
				errorResp.Error.Message, errorResp.Error.Type, errorResp.Error.Code, errorResp.Error.Param, resp.Status)
		}
		return "", fmt.Errorf("Bothub API request failed with status %s and body: %s", resp.Status, string(responseBodyBytes))
	}

	var transcriptionResp model.TranscriptionResponse
	err = json.Unmarshal(responseBodyBytes, &transcriptionResp)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON response from Bothub API (%s): %w. Response body: %s", resp.Status, err, string(responseBodyBytes))
	}

	if transcriptionResp.Error != nil {
		return "", fmt.Errorf("Bothub API returned an error in JSON response: %s (Type: %s)", transcriptionResp.Error.Message, transcriptionResp.Error.Type)
	}
	if transcriptionResp.Text == "" && transcriptionResp.Error == nil {
		log.Printf("Warning: Bothub API returned OK status but no text. Response: %s", string(responseBodyBytes))
		// Не возвращаем ошибку, если текст просто пустой, но нет явной ошибки API.
		// Это может означать тишину в аудио.
	}

	log.Printf("STT: Successfully recognized text: \"%s\"", transcriptionResp.Text)
	return transcriptionResp.Text, nil
}

func convertOgaToWav(ogaPath string, wavPath string) error {
	cmd := exec.Command("ffmpeg", "-i", ogaPath, "-y", "-acodec", "pcm_s16le", "-ar", "16000", "-ac", "1", wavPath)
	output, err := cmd.CombinedOutput()
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

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
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

	ogaTempFile, err := os.CreateTemp("", "voice-*.oga")
	if err != nil {
		log.Printf("Error creating temp oga file: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка сервера: не удалось создать временный файл для аудио."))
		return
	}
	ogaFilePath := ogaTempFile.Name()
	ogaTempFile.Close()
	defer func() {
		log.Printf("Attempting to remove oga file: %s", ogaFilePath)
		if err := os.Remove(ogaFilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("Error removing temp oga file %s: %v", ogaFilePath, err)
		}
	}()

	err = downloadFile(bot, voice.FileID, ogaFilePath)
	if err != nil {
		log.Printf("Error downloading voice file (ID: %s): %v", voice.FileID, err)
		bot.Send(tgbotapi.NewMessage(chatID, "Не удалось скачать голосовое сообщение."))
		return
	}

	wavTempFile, err := os.CreateTemp("", "voice-*.wav")
	if err != nil {
		log.Printf("Error creating temp wav file: %v", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка сервера: не удалось создать временный файл для конвертации."))
		return
	}
	wavFilePath := wavTempFile.Name()
	wavTempFile.Close()
	defer func() {
		log.Printf("Attempting to remove wav file: %s", wavFilePath)
		if err := os.Remove(wavFilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("Error removing temp wav file %s: %v", wavFilePath, err)
		}
	}()

	err = convertOgaToWav(ogaFilePath, wavFilePath)
	if err != nil {
		log.Printf("Error converting audio from %s to %s: %v", ogaFilePath, wavFilePath, err)
		bot.Send(tgbotapi.NewMessage(chatID, "Ошибка конвертации аудио."))
		return
	}

	recognizedText, err := recognizeSpeech(wavFilePath, cfg)
	if err != nil {
		log.Printf("Error recognizing speech for file %s: %v", wavFilePath, err)
		bot.Send(tgbotapi.NewMessage(chatID, "Не удалось распознать речь."))
		return
	}

	msg := tgbotapi.NewMessage(chatID, recognizedText)
	if recognizedText == "" {
		msg.Text = "Не удалось извлечь текст из голосового сообщения (результат пуст)."
	}
	msg.ReplyToMessageID = message.MessageID
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Error sending message to chat %d: %v", chatID, err)
	}
}

// Новая функция для скачивания аудио с YouTube с помощью yt-dlp
func downloadAudioFromYoutube(youtubeURL string, cfg *config.Config) (string, error) { // <--- Добавлен cfg
	//	tempFile, err := os.CreateTemp(os.TempDir(), "youtube_audio_*.mp3")
	tempFile, err := os.CreateTemp("./upload", "youtube_audio_*.mp3")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file for youtube audio name: %w", err)
	}
	mp3FilePath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		log.Printf("Warning: failed to close temp file handle for %s: %v", mp3FilePath, err)
	}
	os.Remove(mp3FilePath)

	log.Printf("Downloading audio from YouTube URL: %s to %s", youtubeURL, mp3FilePath)

	args := []string{
		"-o", mp3FilePath, // путь для сохранения
		"-x", // извлечь аудио
		"--audio-format", "mp3",
		"--no-playlist", // не скачивать плейлист
		"--quiet",       // меньше вывода
		"--no-warnings", // нет предупреждений
	}

	// Добавляем cookies, если путь указан в конфиге
	if cfg.YoutubeCookiesPath != "" {
		// Проверяем, существует ли файл cookies
		if _, err := os.Stat(cfg.YoutubeCookiesPath); err == nil {
			log.Printf("Using YouTube cookies from: %s", cfg.YoutubeCookiesPath)
			args = append(args, "--cookies", cfg.YoutubeCookiesPath)
		} else {
			log.Printf("WARNING: YouTube cookies file specified but not found at %s: %v. Proceeding without cookies.", cfg.YoutubeCookiesPath, err)
		}
	} else {
		log.Println("WARNING: YouTube cookies file not specified in config. Downloads may fail due to bot detection.")
	}

	args = append(args, youtubeURL) // URL всегда последний

	cmd := exec.Command("yt-dlp", args...)

	var stdOutAndErr bytes.Buffer
	cmd.Stdout = &stdOutAndErr
	cmd.Stderr = &stdOutAndErr

	err = cmd.Run()
	if err != nil {
		log.Printf("yt-dlp error for URL %s: %v\nOutput: %s", youtubeURL, err, stdOutAndErr.String())
		if _, statErr := os.Stat(mp3FilePath); statErr == nil {
			os.Remove(mp3FilePath)
		}
		return "", fmt.Errorf("yt-dlp failed: %w. Output: %s", err, stdOutAndErr.String())
	}

	fileInfo, err := os.Stat(mp3FilePath)
	if os.IsNotExist(err) {
		log.Printf("yt-dlp ran but output file %s not found. Output: %s", mp3FilePath, stdOutAndErr.String())
		return "", fmt.Errorf("yt-dlp output file not found: %s. Output: %s", mp3FilePath, stdOutAndErr.String())
	}
	if err != nil {
		log.Printf("Error stating output file %s: %v. Output: %s", mp3FilePath, err, stdOutAndErr.String())
		return "", fmt.Errorf("error stating yt-dlp output file %s: %w. Output: %s", mp3FilePath, err, stdOutAndErr.String())
	}
	if fileInfo.Size() == 0 {
		log.Printf("yt-dlp created an empty file %s. Output: %s", mp3FilePath, stdOutAndErr.String())
		os.Remove(mp3FilePath)
		return "", fmt.Errorf("yt-dlp created an empty file: %s. Output: %s", mp3FilePath, stdOutAndErr.String())
	}

	log.Printf("Successfully downloaded audio to %s (size: %d bytes)", mp3FilePath, fileInfo.Size())
	return mp3FilePath, nil
}

// Новая функция для запроса к Bothub Chat Completions API
func getChatCompletionFromBothub(text string, cfg *config.Config) (string, error) {
	log.Printf("Requesting chat completion from Bothub for text starting with: %.80s...", text)

	// Формируем контент для запроса.
	// Согласно заданию, распознанный текст передается в поле content.
	// Чтобы получить осмысленную информацию *о видео* на основе этого текста,
	// лучше сформулировать запрос к модели.
	userContent := "Проанализируй следующий текст, который был извлечен из аудиодорожки YouTube видео, и предоставь краткое содержание или ключевые моменты этого видео (отвечай на русском языке):\n\n\"" + text + "\""
	// Если строго следовать "текст передается в content", то userContent = text.
	// Однако, API ожидает инструкцию в 'content', как в примере "Tell me about Fiji".
	// Мой вариант userContent является такой инструкцией, включающей текст.

	requestPayload := model.ChatCompletionRequest{
		Model: gptModelForYoutubeSummary,
		Messages: []model.ChatMessage{
			{
				Role:    "user",
				Content: userContent,
			},
		},
	}

	requestBodyBytes, err := json.Marshal(requestPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal chat completion request: %w", err)
	}

	req, err := http.NewRequest("POST", bothubChatCompletionsApiURL, bytes.NewBuffer(requestBodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create new HTTP request for chat completion: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.BothubApiToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 120 * time.Second} // Таймаут для LLM может быть длинным
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to execute HTTP request to Bothub Chat API: %w", err)
	}
	defer resp.Body.Close()

	responseBodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body from Bothub Chat API: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("Bothub Chat API returned non-OK status: %s. Response: %s", resp.Status, string(responseBodyBytes))
		var errorResp model.ChatCompletionResponse
		if json.Unmarshal(responseBodyBytes, &errorResp) == nil && errorResp.Error != nil {
			return "", fmt.Errorf("Bothub Chat API error: %s (Type: %s, Code: %s, Param: %s), HTTP Status: %s",
				errorResp.Error.Message, errorResp.Error.Type, errorResp.Error.Code, errorResp.Error.Param, resp.Status)
		}
		return "", fmt.Errorf("Bothub Chat API request failed with status %s and body: %s", resp.Status, string(responseBodyBytes))
	}

	var chatResponse model.ChatCompletionResponse
	err = json.Unmarshal(responseBodyBytes, &chatResponse)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal JSON response from Bothub Chat API (%s): %w. Response body: %s", resp.Status, err, string(responseBodyBytes))
	}

	if chatResponse.Error != nil {
		return "", fmt.Errorf("Bothub Chat API returned an error in JSON response: %s (Type: %s)", chatResponse.Error.Message, chatResponse.Error.Type)
	}

	if len(chatResponse.Choices) == 0 || chatResponse.Choices[0].Message.Content == "" {
		log.Printf("Warning: Bothub Chat API returned OK status but no content. Response: %s", string(responseBodyBytes))
		return "", fmt.Errorf("Bothub Chat API returned no content in response. Response body: %s", string(responseBodyBytes))
	}

	log.Printf("Bothub Chat API successfully returned completion.")
	return chatResponse.Choices[0].Message.Content, nil
}

func handleYoutubeVideoInfoProcessing(bot *tgbotapi.BotAPI, message *tgbotapi.Message, cfg *config.Config) {
	chatID := message.Chat.ID
	youtubeURL := message.Text

	processingMsg := tgbotapi.NewMessage(chatID, "Получил ссылку, начинаю обработку видео. Это может занять некоторое время...")
	processingMsg.ReplyToMessageID = message.MessageID
	sentMsg, err := bot.Send(processingMsg)
	var messageIDToEdit int
	if err == nil && sentMsg.MessageID != 0 {
		messageIDToEdit = sentMsg.MessageID
	} else if err != nil {
		log.Printf("Error sending processing message: %v", err)
	}

	// 1. Скачать аудио с YouTube
	mp3FilePath, err := downloadAudioFromYoutube(youtubeURL, cfg)
	if err != nil {
		log.Printf("Error downloading audio from YouTube %s: %v", youtubeURL, err)
		replyText := fmt.Sprintf("Не удалось скачать аудио из видео: %v", err)
		sendOrEditMessage(bot, chatID, messageIDToEdit, replyText, message.MessageID)
		return
	}
	defer func() {
		log.Printf("Attempting to remove YouTube audio file: %s", mp3FilePath)
		if errRem := os.Remove(mp3FilePath); errRem != nil && !os.IsNotExist(errRem) {
			log.Printf("Error removing temp YouTube audio file %s: %v", mp3FilePath, errRem)
		}
	}()

	sendOrEditMessage(bot, chatID, messageIDToEdit, "Аудио извлечено, распознаю речь...", 0)

	// 2. Распознать речь из аудиофайла
	recognizedText, err := recognizeSpeech(mp3FilePath, cfg)
	if err != nil {
		log.Printf("Error recognizing speech from YouTube audio %s (file: %s): %v", youtubeURL, mp3FilePath, err)
		replyText := fmt.Sprintf("Не удалось распознать речь из видео: %v", err)
		sendOrEditMessage(bot, chatID, messageIDToEdit, replyText, message.MessageID)
		return
	}

	if recognizedText == "" {
		log.Printf("Recognized text is empty for YouTube audio %s (file: %s)", youtubeURL, mp3FilePath)
		replyText := "Не удалось извлечь текст из видео (результат распознавания пуст)."
		sendOrEditMessage(bot, chatID, messageIDToEdit, replyText, message.MessageID)
		return
	}

	sendOrEditMessage(bot, chatID, messageIDToEdit, "Текст из видео получен, запрашиваю информацию у нейросети...", 0)

	// 3. Передать текст в Bothub Chat Completions API
	summary, err := getChatCompletionFromBothub(recognizedText, cfg)
	if err != nil {
		log.Printf("Error getting info from Bothub Chat API for YouTube video %s: %v", youtubeURL, err)
		replyText := fmt.Sprintf("Не удалось получить информацию о видео от нейросети: %v", err)
		sendOrEditMessage(bot, chatID, messageIDToEdit, replyText, message.MessageID)
		return
	}

	// 4. Отправить результат пользователю
	finalReply := fmt.Sprintf("Информация о видео (на основе аудиодорожки):\n\n%s", summary)
	sendOrEditMessage(bot, chatID, messageIDToEdit, finalReply, message.MessageID)
}

// Вспомогательная функция для отправки или редактирования сообщения
func sendOrEditMessage(bot *tgbotapi.BotAPI, chatID int64, messageIDToEdit int, text string, replyToMessageID int) {
	var chattable tgbotapi.Chattable
	if messageIDToEdit != 0 {
		editMsg := tgbotapi.NewEditMessageText(chatID, messageIDToEdit, text)
		if len(editMsg.Text) > maxMessageTextLength {
			editMsg.Text = editMsg.Text[:maxMessageTextLength-3] + "..."
		}
		chattable = editMsg
	} else {
		newMsg := tgbotapi.NewMessage(chatID, text)
		if replyToMessageID != 0 { // Отвечаем на исходное сообщение, если не редактируем
			newMsg.ReplyToMessageID = replyToMessageID
		}
		if len(newMsg.Text) > maxMessageTextLength {
			newMsg.Text = newMsg.Text[:maxMessageTextLength-3] + "..."
		}
		chattable = newMsg
	}

	if _, err := bot.Send(chattable); err != nil {
		log.Printf("Error sending/editing message to chat %d: %v", chatID, err)
		// Если редактирование не удалось, можно попробовать отправить новое сообщение
		if messageIDToEdit != 0 {
			log.Printf("Editing failed for chat %d, attempting to send as new message.", chatID)
			newMsgFallback := tgbotapi.NewMessage(chatID, text)
			if replyToMessageID != 0 {
				newMsgFallback.ReplyToMessageID = replyToMessageID
			}
			if len(newMsgFallback.Text) > maxMessageTextLength {
				newMsgFallback.Text = newMsgFallback.Text[:maxMessageTextLength-3] + "..."
			}
			if _, fallbackErr := bot.Send(newMsgFallback); fallbackErr != nil {
				log.Printf("Error sending fallback message to chat %d: %v", chatID, fallbackErr)
			}
		}
	}
}

func sendMainMenu(bot *tgbotapi.BotAPI, chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "Выберите опцию, отправьте голосовое сообщение или ссылку на Youtube-видео:")
	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(menuCommandRecognize),
			tgbotapi.NewKeyboardButton(menuCommandInfo),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(menuCommandSettings),
			tgbotapi.NewKeyboardButton(menuCommandYoutubeInfo),
		),
	)
	keyboard.ResizeKeyboard = true
	msg.ReplyMarkup = keyboard
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Error sending main menu: %v", err)
	}
}

func checkDependencies() {
	missingDeps := []string{}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		log.Println("WARNING: yt-dlp not found in PATH. Youtube video processing will fail.")
		missingDeps = append(missingDeps, "yt-dlp")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Println("WARNING: ffmpeg not found in PATH. Voice message and Youtube video processing may fail.")
		missingDeps = append(missingDeps, "ffmpeg")
	}

	if len(missingDeps) == 0 {
		log.Println("Dependencies (yt-dlp, ffmpeg) checked successfully.")
	} else {
		log.Printf("Please install missing dependencies: %v", missingDeps)
	}
}

func main() {
	cfg := &config.Config{}
	if err := coreconfig.Load(cfg, ""); err != nil {
		log.Panic("Can't load config file: ", err)
	}

	botToken := cfg.TelegramBotToken
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN environment variable not set")
	}
	if cfg.BothubApiToken == "" {
		log.Fatal("BOTHUB_API_TOKEN environment variable not set in config")
	}
	if cfg.YoutubeCookiesPath == "" {
		log.Println("INFO: YOUTUBE_COOKIES_PATH is not set in config. YouTube video downloads might be restricted or fail due to bot detection. It is recommended to provide a cookies.txt file for reliable operation.")
	} else {
		if _, err := os.Stat(cfg.YoutubeCookiesPath); os.IsNotExist(err) {
			log.Printf("WARNING: YOUTUBE_COOKIES_PATH is set to '%s', but the file was not found. YouTube video downloads might fail.", cfg.YoutubeCookiesPath)
		} else {
			log.Printf("INFO: Using YouTube cookies from: %s", cfg.YoutubeCookiesPath)
		}
	}

	checkDependencies() // Проверка наличия yt-dlp и ffmpeg

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Fatalf("NewBotAPI error: %v", err) // Используем Fatalf для единого стиля
	}

	bot.Debug = true // Установить в false для продакшена
	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)
	semaphore := make(chan struct{}, concurrencyLimit)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		go func(currentUpdate tgbotapi.Update) {
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			message := currentUpdate.Message
			chatID := message.Chat.ID

			if message.IsCommand() {
				switch message.Command() {
				case "start", "menu":
					sendMainMenu(bot, chatID)
				default:
					msg := tgbotapi.NewMessage(chatID, "Неизвестная команда. Используйте /start или /menu для отображения меню.")
					bot.Send(msg)
				}
				return
			}

			isHandled := false
			switch message.Text {
			case menuCommandRecognize:
				msg := tgbotapi.NewMessage(chatID, "Пожалуйста, отправьте мне голосовое сообщение для распознавания.")
				bot.Send(msg)
				isHandled = true
			case menuCommandInfo:
				msgText := "Я бот для обработки аудио и видео.\n"
				msgText += "- Распознаю речь из голосовых сообщений.\n"
				msgText += "- Предоставляю информацию о Youtube-видео (на основе аудиодорожки).\n"
				msgText += "Используется API от bothub.chat.\n"
				msgText += "Разработчик: Pomogalov Vladimir (доработано AI)\n"
				msgText += "Версия: 0.2.0"
				msg := tgbotapi.NewMessage(chatID, msgText)
				bot.Send(msg)
				isHandled = true
			case menuCommandSettings:
				msg := tgbotapi.NewMessage(chatID, "Раздел настроек пока в разработке.")
				bot.Send(msg)
				isHandled = true
			case menuCommandYoutubeInfo:
				msg := tgbotapi.NewMessage(chatID, "Пожалуйста, отправьте мне ссылку на Youtube-видео.")
				bot.Send(msg)
				isHandled = true
			default:
				if isValidYoutubeLink(message.Text) {
					handleYoutubeVideoInfoProcessing(bot, message, cfg)
					isHandled = true
				}
			}

			if message.Voice != nil {
				handleVoiceMessage(bot, message, cfg)
				isHandled = true
			}

			if !isHandled && message.Text != "" { // Если это не команда, не кнопка, не ссылка, не голосовое
				log.Printf("[%s] (ChatID: %d) sent unhandled text: %s", message.From.UserName, chatID, message.Text)
				msg := tgbotapi.NewMessage(chatID, "Я не совсем понял. Может, выберете что-то из меню, отправите голосовое сообщение или ссылку на Youtube?")
				msg.ReplyToMessageID = message.MessageID
				bot.Send(msg)
				sendMainMenu(bot, chatID)
			}
		}(update)
	}
}
