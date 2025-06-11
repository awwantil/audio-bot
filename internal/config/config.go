package config

type Config struct {
	TelegramBotToken   string `envconfig:"TELEGRAM_BOT_TOKEN" default:"1s"`
	BothubApiToken     string `envconfig:"BOTHUB_API_TOKEN" default:"1sds33s"`
	YoutubeCookiesPath string `envconfig:"YOUTUBE_COOKIES_PATH" default:"./upload/cookies.txt"`
}
