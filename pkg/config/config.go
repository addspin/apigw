package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config представляет конфигурацию приложения
type Config struct {
	Server   ServerConfig   `json:"server"`
	Services ServicesConfig `json:"services"`
}

// ServerConfig представляет конфигурацию сервера
type ServerConfig struct {
	Port int `json:"port"`
}

// ServicesConfig представляет конфигурацию внешних сервисов
type ServicesConfig struct {
	News     ServiceConfig `json:"news"`
	Comments ServiceConfig `json:"comments"`
}

// ServiceConfig представляет конфигурацию отдельного сервиса
type ServiceConfig struct {
	URL string `json:"url"`
}

// LoadConfig загружает конфигурацию из файла
func LoadConfig(filename string) (*Config, error) {
	// Задаем конфигурацию по умолчанию
	cfg := NewConfig()

	// Открываем файл конфигурации
	file, err := os.Open(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// Если файл не существует, создаем его с конфигурацией по умолчанию
			file, err := os.Create(filename)
			if err != nil {
				return nil, fmt.Errorf("не удалось создать файл конфигурации: %w", err)
			}
			defer file.Close()

			encoder := json.NewEncoder(file)
			encoder.SetIndent("", "    ")
			if err := encoder.Encode(cfg); err != nil {
				return nil, fmt.Errorf("не удалось записать конфигурацию по умолчанию: %w", err)
			}

			return cfg, nil
		}
		return nil, fmt.Errorf("не удалось открыть файл конфигурации: %w", err)
	}
	defer file.Close()

	// Декодируем JSON
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("не удалось декодировать конфигурацию: %w", err)
	}

	return cfg, nil
}

// NewConfig создает новый экземпляр конфигурации с значениями по умолчанию
func NewConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8081,
		},
		Services: ServicesConfig{
			News: ServiceConfig{
				URL: "http://localhost:8080",
			},
			Comments: ServiceConfig{
				URL: "http://localhost:8082",
			},
		},
	}
}
