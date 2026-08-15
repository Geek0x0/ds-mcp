package main

import (
	"log"
	"os"

	"ds-mcp/internal/deepseek"
	dsserver "ds-mcp/internal/server"
)

const version = "0.1.0"

func main() {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		log.Fatal("DEEPSEEK_API_KEY is required")
	}
	baseURL := os.Getenv("DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com"
	}
	if err := dsserver.New(deepseek.New(key, baseURL), version).ServeStdio(); err != nil {
		log.Fatal(err)
	}
}
