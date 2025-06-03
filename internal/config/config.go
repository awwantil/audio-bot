package config

type Config struct {
	TelegramBotToken string `envconfig:"TELEGRAM_BOT_TOKEN" default:"1s"`
}
