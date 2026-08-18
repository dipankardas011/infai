package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type EngineInvocationMethod string

const (
	// we get the normal TUI/UI and other option bro.
	EngineInvocationMethodHumanInLoop EngineInvocationMethod = "human_in_loop"
	// We get the proper Workflow thing.
	// NOTE: for now no logic for this
	EngineInvocationMethodWorkflow EngineInvocationMethod = "workflow"
)

type AgentEngineConfig struct {
	Host          string `mapstructure:"host"`
	Port          int    `mapstructure:"port"`
	EnableHealthz bool   `mapstructure:"enable_healthz"`
	OpenTelemetry struct {
		ServiceName    string  `mapstructure:"service_name"`
		ServiceVersion string  `mapstructure:"service_version"`
		OTLPEndpoint   string  `mapstructure:"otlp_endpoint"`
		SamplingRatio  float64 `mapstructure:"sampling_ratio"`
	} `mapstructure:"opentelemetry"`
	Engine struct {
		InvocationMethod EngineInvocationMethod `mapstructure:"invocation_method"`
	} `mapstructure:"engine"`
	Logging struct {
		Level string `mapstructure:"level"`
	} `mapstructure:"logging"`
}

// The Primary agent gets all of these configuration but for subagent inheritable we need to TBD

func LoadConfig() (*AgentEngineConfig, error) {
	viper.SetEnvPrefix("INFAI_AGENT")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	_ = viper.BindEnv("host")
	_ = viper.BindEnv("port")

	// Bind OpenTelemetry configuration
	_ = viper.BindEnv("opentelemetry.service_name")
	_ = viper.BindEnv("opentelemetry.service_version")
	_ = viper.BindEnv("opentelemetry.otlp_endpoint")
	_ = viper.BindEnv("opentelemetry.sampling_ratio")

	// Bind Logging configuration
	_ = viper.BindEnv("logging.level")

	// Bind Healthz configuration
	_ = viper.BindEnv("enable_healthz")

	//  Bind Invocation Method
	_ = viper.BindEnv("engine.invocation_method")

	viper.SetDefault("enable_metrics", false)
	viper.SetDefault("enable_healthz", false)

	viper.SetDefault("host", "localhost")
	viper.SetDefault("port", 8080)
	viper.SetDefault("enable_metrics", false)
	viper.SetDefault("enable_healthz", true)

	viper.SetDefault("opentelemetry.service_name", "infai-agent")
	viper.SetDefault("opentelemetry.service_version", "v0.0.1")
	viper.SetDefault("opentelemetry.otlp_endpoint", "localhost:4317") // Standard OTLP gRPC port
	viper.SetDefault("opentelemetry.sampling_ratio", 1.0)
	viper.SetDefault("logging.level", "info")

	viper.SetDefault("engine.invocation_method", EngineInvocationMethodHumanInLoop)

	v := &AgentEngineConfig{}
	err := viper.Unmarshal(v)
	if err != nil {
		return nil, err
	}

	if err := v.Validate(); err != nil {
		return nil, err
	}

	return v, err
}

func (c *AgentEngineConfig) Validate() error {

	switch {
	case c.OpenTelemetry.SamplingRatio < 0, c.OpenTelemetry.SamplingRatio > 1:
		return fmt.Errorf("Invalid sampling ratio: %f", c.OpenTelemetry.SamplingRatio)
	case c.OpenTelemetry.ServiceName == "":
		return fmt.Errorf("Service name is required")
	case c.OpenTelemetry.ServiceVersion == "":
		return fmt.Errorf("Service version is required")
	case c.Engine.InvocationMethod != EngineInvocationMethodHumanInLoop:
		return fmt.Errorf("Invalid invocation method: %s", c.Engine.InvocationMethod)
	case c.Logging.Level != "info" && c.Logging.Level != "debug":
		return fmt.Errorf("Invalid logging level: %s", c.Logging.Level)

	default:
		return nil
	}
}
