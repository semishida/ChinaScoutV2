package main

import (
	"log"
	"os"

	"csv2/bot"
	"csv2/ranking"
	"github.com/joho/godotenv"
)

func main() {
	// Загрузка .env
	err := godotenv.Load()
	if err != nil {
		log.Printf("Warning: Failed to load .env file: %v", err)
	}

	token := os.Getenv("DISCORD_TOKEN")
	floodChannelID := os.Getenv("FLOOD_CHANNEL_ID")
	relayChannelID := os.Getenv("RELAY_CHANNEL_ID")
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	adminFilePath := "admins.json"
	redisAddr := "localhost:6379"

	// Проверка токена
	if token == "" {
		log.Fatal("DISCORD_TOKEN is not set")
	}

	rank, err := ranking.NewRanking(adminFilePath, redisAddr)
	if err != nil {
		log.Fatalf("Failed to initialize ranking: %v", err)
	}

	discord := bot.SetupDiscord(token, floodChannelID, relayChannelID, rank)
	bot.SetupTelegram(telegramToken, floodChannelID, relayChannelID, discord, rank)

	select {}
}
