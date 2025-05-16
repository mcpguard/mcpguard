package config

import (
	"github.com/google/uuid"
	"os"
	"strconv"
)

type Config struct {
	AgentID    string
	ServerPort int
	Docker     bool   // true if running in Docker
	APIKey     string // API key for agent authentication
}

func NewConfig() *Config {
	return &Config{
		AgentID:    uuid.NewString(),
		ServerPort: getEnvAsInt("SERVER_PORT", 11435),
		Docker:     getEnv("DOCKER", "false") == "true",
		APIKey:     getEnv("MCPGUARD_API_KEY", ""),
	}
}

// Helper function to read environment variables with defaults
func getEnvAsInt(key string, defaultVal int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return defaultVal
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
