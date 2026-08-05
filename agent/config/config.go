package config

import (
	"strings"

	"github.com/spf13/viper"
)

type HarnessConfig struct {
	Host          string `mapstructure:"host"`
	Port          int    `mapstructure:"port"`
	EnableHealthz bool   `mapstructure:"enable_healthz"`
	OpenTelemetry struct {
		ServiceName      string  `mapstructure:"service_name"`
		ServiceNamespace string  `mapstructure:"service_namespace"`
		ServiceVersion   string  `mapstructure:"service_version"`
		OTLPEndpoint     string  `mapstructure:"otlp_endpoint"`
		SamplingRatio    float64 `mapstructure:"sampling_ratio"`
	} `mapstructure:"opentelemetry"`
	Logging struct {
		Level string `mapstructure:"level"`
	} `mapstructure:"logging"`
}

// The Primary agent gets all of these configuration but for subagent inheritable we need to TBD

func LoadConfig() (*HarnessConfig, error) {
	viper.SetEnvPrefix("INFAI")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	_ = viper.BindEnv("host")
	_ = viper.BindEnv("port")

	// Bind OpenTelemetry configuration
	_ = viper.BindEnv("opentelemetry.service_name")
	_ = viper.BindEnv("opentelemetry.service_namespace")
	_ = viper.BindEnv("opentelemetry.service_version")
	_ = viper.BindEnv("opentelemetry.otlp_endpoint")
	_ = viper.BindEnv("opentelemetry.sampling_ratio")

	// Bind Logging configuration
	_ = viper.BindEnv("logging.level")

	// Bind Healthz configuration
	_ = viper.BindEnv("enable_healthz")

	viper.SetDefault("enable_metrics", false)
	viper.SetDefault("enable_healthz", false)

	viper.SetDefault("host", "localhost")
	viper.SetDefault("port", 8080)
	viper.SetDefault("enable_metrics", false)
	viper.SetDefault("enable_healthz", true)

	viper.SetDefault("opentelemetry.service_name", "infai-agent")
	viper.SetDefault("opentelemetry.service_namespace", "infai")
	viper.SetDefault("opentelemetry.service_version", "v0.0.1")
	viper.SetDefault("opentelemetry.otlp_endpoint", "localhost:4317") // Standard OTLP gRPC port
	viper.SetDefault("opentelemetry.sampling_ratio", 1.0)
	viper.SetDefault("logging.level", "info")

	v := &HarnessConfig{}
	err := viper.Unmarshal(v)
	if err != nil {
		panic("Failed to unmarshal configuration: " + err.Error())
	}

	return v, err
}
