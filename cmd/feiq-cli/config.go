package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type appConfig struct {
	Bind         *string `json:"bind"`
	Port         *int    `json:"port"`
	Name         *string `json:"name"`
	Host         *string `json:"host"`
	Version      *string `json:"version"`
	Output       *string `json:"output"`
	HistoryFile  *string `json:"history_file"`
	Color        *string `json:"color"`
	MessageWait  *string `json:"message_wait"`
	TransferWait *string `json:"transfer_wait"`
}

func loadAppConfig(args []string) (appConfig, string, error) {
	path, explicit, err := configPathFromArgs(args)
	if err != nil {
		return appConfig{}, "", err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return appConfig{}, path, nil
	}
	if err != nil {
		return appConfig{}, path, fmt.Errorf("open config %s: %w", path, err)
	}
	defer file.Close()

	var config appConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return appConfig{}, path, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return appConfig{}, path, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := config.validate(); err != nil {
		return appConfig{}, path, fmt.Errorf("invalid config %s: %w", path, err)
	}
	config.expandPaths()
	return config, path, nil
}

func configPathFromArgs(args []string) (string, bool, error) {
	var path string
	explicit := false
	for index, arg := range args {
		if arg == "--config" {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return "", true, fmt.Errorf("--config requires a path")
			}
			path = expandHomePath(args[index+1])
			explicit = true
		}
		if value, ok := strings.CutPrefix(arg, "--config="); ok {
			if value == "" {
				return "", true, fmt.Errorf("--config requires a path")
			}
			path = expandHomePath(value)
			explicit = true
		}
	}
	if explicit {
		return path, true, nil
	}
	defaultPath, err := defaultConfigPath()
	if err != nil {
		return "", false, err
	}
	return defaultPath, false, nil
}

func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory for config: %w", err)
	}
	return filepath.Join(home, ".feiq-cli", "config.json"), nil
}

func expandHomePath(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func (config appConfig) validate() error {
	if config.Port != nil && (*config.Port < 1 || *config.Port > 65535) {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if config.Color != nil && *config.Color != "auto" && *config.Color != "always" && *config.Color != "never" {
		return fmt.Errorf("color must be auto, always or never")
	}
	if config.MessageWait != nil {
		if _, err := positiveDuration("message_wait", *config.MessageWait); err != nil {
			return err
		}
	}
	if config.TransferWait != nil {
		if _, err := positiveDuration("transfer_wait", *config.TransferWait); err != nil {
			return err
		}
	}
	return nil
}

func (config *appConfig) expandPaths() {
	if config.Output != nil {
		expanded := expandHomePath(*config.Output)
		config.Output = &expanded
	}
	if config.HistoryFile != nil {
		expanded := expandHomePath(*config.HistoryFile)
		config.HistoryFile = &expanded
	}
}

func positiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return duration, nil
}

func configDuration(value *string, fallback time.Duration) time.Duration {
	if value == nil {
		return fallback
	}
	duration, _ := time.ParseDuration(*value)
	return duration
}

func configString(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func configInt(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func addConfigFlag(fs *flag.FlagSet, path string) {
	fs.String("config", path, "JSON configuration file")
}
