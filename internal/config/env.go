package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

const (
	envServerPort                 = "LLM_PROXY_SERVER_PORT"
	envServerShowBaseURL          = "LLM_PROXY_SERVER_SHOW_BASE_URL"
	envLogLevel                   = "LLM_PROXY_LOG_LEVEL"
	envLogFile                    = "LLM_PROXY_LOG_FILE"
	envLogMaxAge                  = "LLM_PROXY_LOG_MAX_AGE"
	envRateLimitEnabled           = "LLM_PROXY_RATE_LIMIT_ENABLED"
	envRateLimitRequestsPerSecond = "LLM_PROXY_RATE_LIMIT_DEFAULT_REQUESTS_PER_SECOND"
	envRateLimitBurst             = "LLM_PROXY_RATE_LIMIT_DEFAULT_BURST"
	envOpenAIBaseURL              = "LLM_PROXY_PROVIDERS_OPENAI_BASE_URL"
	envAnthropicBaseURL           = "LLM_PROXY_PROVIDERS_ANTHROPIC_BASE_URL"
)

type envBinding struct {
	key      string
	name     string
	validate func(string) error
}

var envBindings = []envBinding{
	{key: "server.port", name: envServerPort, validate: validateInt},
	{key: "server.show_base_url", name: envServerShowBaseURL},
	{key: "log.level", name: envLogLevel},
	{key: "log.file", name: envLogFile},
	{key: "log.max_age", name: envLogMaxAge, validate: validateInt},
	{key: "rate_limit.enabled", name: envRateLimitEnabled, validate: validateBool},
	{key: "rate_limit.default.requests_per_second", name: envRateLimitRequestsPerSecond, validate: validateFloat},
	{key: "rate_limit.default.burst", name: envRateLimitBurst, validate: validateInt},
	{key: "providers.openai.base_url", name: envOpenAIBaseURL},
	{key: "providers.anthropic.base_url", name: envAnthropicBaseURL},
}

func loadDotEnv() error {
	err := godotenv.Load()
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("load .env: %w", err)
}

func bindEnvironment(v *viper.Viper) error {
	for _, binding := range envBindings {
		if value, ok := os.LookupEnv(binding.name); ok && binding.validate != nil {
			if err := binding.validate(value); err != nil {
				return fmt.Errorf("%s: %w", binding.name, err)
			}
		}
		if err := v.BindEnv(binding.key, binding.name); err != nil {
			return fmt.Errorf("bind %s: %w", binding.name, err)
		}
	}
	return nil
}

func validateInt(value string) error {
	_, err := strconv.Atoi(value)
	return err
}

func validateBool(value string) error {
	_, err := strconv.ParseBool(value)
	return err
}

func validateFloat(value string) error {
	_, err := strconv.ParseFloat(value, 64)
	return err
}
