package launcher

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dipankardas011/infai/model"
	"github.com/dipankardas011/infai/runner"
)

func BuildSpec(engine model.InferenceEngine, m model.ModelEntry, p model.Profile) (runner.LaunchSpec, error) {
	if engine.Path == "" {
		return runner.LaunchSpec{}, fmt.Errorf("inference engine executable is empty")
	}
	switch engine.Kind {
	case "", model.EngineLlamaCPP:
		return BuildLlamaCPPSpec(engine, m, p)
	case model.EngineVLLM:
		return BuildVLLMSpec(engine, m, p)
	default:
		return runner.LaunchSpec{}, fmt.Errorf("unsupported inference engine kind %q", engine.Kind)
	}
}

func BuildLlamaCPPSpec(engine model.InferenceEngine, m model.ModelEntry, p model.Profile) (runner.LaunchSpec, error) {
	if m.Type != model.TypeGGUF && m.Type != model.TypeGGUFMultimodal {
		return runner.LaunchSpec{}, fmt.Errorf("llama.cpp requires a GGUF model, got %s", m.Type)
	}
	args, err := buildArgs(engine.Path, m, p)
	if err != nil {
		return runner.LaunchSpec{}, err
	}
	launchArgs := append(append([]string(nil), engine.BaseArgs...), args[1:]...)
	return runner.LaunchSpec{Command: args[0], Args: launchArgs, Env: engine.Env}, nil
}

func BuildVLLMSpec(engine model.InferenceEngine, m model.ModelEntry, p model.Profile) (runner.LaunchSpec, error) {
	modelPath := strings.TrimSpace(m.ModelPath())
	if modelPath == "" {
		return runner.LaunchSpec{}, fmt.Errorf("vLLM model path is empty")
	}
	if m.Type != model.TypeSafetensors && m.Type != model.TypeHFQuantized {
		return runner.LaunchSpec{}, fmt.Errorf("vLLM requires a Hugging Face/safetensors model directory, got %s", m.Type)
	}
	cfg, err := p.VLLMConfig()
	if err != nil {
		return runner.LaunchSpec{}, fmt.Errorf("decode vLLM configuration: %w", err)
	}
	args := append([]string(nil), engine.BaseArgs...)
	if len(args) == 0 {
		args = append(args, "serve")
	}
	args = append(args, modelPath,
		"--host", p.Host,
		"--port", strconv.Itoa(p.Port),
		"--max-model-len", strconv.Itoa(p.ContextSize),
	)
	if cfg.GPUUtilization != nil {
		args = append(args, "--gpu-memory-utilization", strconv.FormatFloat(*cfg.GPUUtilization, 'f', -1, 64))
	}
	if cfg.MaxNumSeqs != nil {
		args = append(args, "--max-num-seqs", strconv.Itoa(*cfg.MaxNumSeqs))
	}
	if cfg.MaxBatchedTokens != nil {
		args = append(args, "--max-num-batched-tokens", strconv.Itoa(*cfg.MaxBatchedTokens))
	}
	if cfg.DType != "" {
		args = append(args, "--dtype", cfg.DType)
	}
	if cfg.TensorParallelSize != nil {
		args = append(args, "--tensor-parallel-size", strconv.Itoa(*cfg.TensorParallelSize))
	}
	if cfg.PipelineParallelSize != nil {
		args = append(args, "--pipeline-parallel-size", strconv.Itoa(*cfg.PipelineParallelSize))
	}
	if cfg.EnablePrefixCaching {
		args = append(args, "--enable-prefix-caching")
	}
	if cfg.TrustRemoteCode {
		args = append(args, "--trust-remote-code")
	}
	if cfg.ServedModelName != "" {
		args = append(args, "--served-model-name", cfg.ServedModelName)
	}
	if p.ExtraFlags != "" {
		extra, err := ParseExtraFlags(p.ExtraFlags)
		if err != nil {
			return runner.LaunchSpec{}, fmt.Errorf("parse extra flags: %w", err)
		}
		if err := rejectManagedFlags(extra, vllmManagedFlags); err != nil {
			return runner.LaunchSpec{}, err
		}
		args = append(args, extra...)
	}
	return runner.LaunchSpec{Command: engine.Path, Args: args, Env: engine.Env}, nil
}

func BuildArgs(serverBin string, m model.ModelEntry, p model.Profile) []string {
	args, _ := buildArgs(serverBin, m, p)
	return args
}

func buildArgs(serverBin string, m model.ModelEntry, p model.Profile) ([]string, error) {
	args := []string{serverBin, "-m", m.ModelPath()}

	if p.UseMmproj && m.MmprojPath != "" {
		args = append(args, "--mmproj", m.MmprojPath)
	}

	args = append(args,
		"--port", strconv.Itoa(p.Port),
		"--host", p.Host,
		"-c", strconv.Itoa(p.ContextSize),
		"-ngl", p.NGL,
		"--metrics",
	)

	if p.BatchSize != nil {
		args = append(args, "-b", strconv.Itoa(*p.BatchSize))
	}
	if p.UBatchSize != nil {
		args = append(args, "-ub", strconv.Itoa(*p.UBatchSize))
	}
	if p.CacheTypeK != nil {
		args = append(args, "--cache-type-k", *p.CacheTypeK)
	}
	if p.CacheTypeV != nil {
		args = append(args, "--cache-type-v", *p.CacheTypeV)
	}
	if p.FlashAttn {
		args = append(args, "--flash-attn", "on")
	}
	if p.Jinja {
		args = append(args, "--jinja")
	}
	if p.Temperature != nil {
		args = append(args, "--temperature", strconv.FormatFloat(*p.Temperature, 'f', -1, 64))
	}
	if p.ReasoningBudget != nil {
		args = append(args, "--reasoning-budget", strconv.Itoa(*p.ReasoningBudget))
	}
	if p.TopP != nil {
		args = append(args, "--top_p", strconv.FormatFloat(*p.TopP, 'f', -1, 64))
	}
	if p.TopK != nil {
		args = append(args, "--top_k", strconv.Itoa(*p.TopK))
	}
	if p.NoKVOffload {
		args = append(args, "--no-kv-offload")
	}
	if p.ExtraFlags != "" {
		extra, err := ParseExtraFlags(p.ExtraFlags)
		if err != nil {
			return nil, fmt.Errorf("parse extra flags: %w", err)
		}
		if err := rejectManagedFlags(extra, llamaManagedFlags); err != nil {
			return nil, err
		}
		args = append(args, extra...)
	}

	return args, nil
}

func BuildCommand(serverBin string, m model.ModelEntry, p model.Profile) string {
	args, err := buildArgs(serverBin, m, p)
	if err != nil {
		return ""
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			quoted[i] = fmt.Sprintf("%q", a)
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}

var llamaManagedFlags = map[string]bool{
	"-m": true, "--mmproj": true, "--port": true, "--host": true, "-c": true, "-ngl": true,
	"-b": true, "-ub": true, "--cache-type-k": true, "--cache-type-v": true, "--flash-attn": true,
	"--jinja": true, "--temperature": true, "--temp": true, "--reasoning-budget": true,
	"--top_p": true, "--top-p": true, "--top_k": true, "--top-k": true, "--no-kv-offload": true, "--metrics": true,
}

var vllmManagedFlags = map[string]bool{
	"--host": true, "--port": true, "--max-model-len": true, "--gpu-memory-utilization": true,
	"--max-num-seqs": true, "--max-num-batched-tokens": true, "--dtype": true,
	"--tensor-parallel-size": true, "--pipeline-parallel-size": true, "--enable-prefix-caching": true,
	"--trust-remote-code": true, "--served-model-name": true,
}

func rejectManagedFlags(args []string, managed map[string]bool) error {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		flag := arg
		if i := strings.IndexByte(flag, '='); i >= 0 {
			flag = flag[:i]
		}
		if managed[flag] {
			return fmt.Errorf("extra flags contain managed option %q; edit the profile option instead", flag)
		}
	}
	return nil
}

// ParseExtraFlags preserves quoted values and is intentionally shell-free.
func ParseExtraFlags(value string) ([]string, error) {
	var args []string
	var current strings.Builder
	var quote byte
	escaped := false
	inToken := false
	for i := 0; i < len(value); i++ {
		c := value[i]
		if escaped {
			current.WriteByte(c)
			escaped = false
			inToken = true
			continue
		}
		if c == '\\' {
			escaped = true
			inToken = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				current.WriteByte(c)
			}
			inToken = true
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			inToken = true
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' {
			if inToken {
				args = append(args, current.String())
				current.Reset()
				inToken = false
			}
			continue
		}
		current.WriteByte(c)
		inToken = true
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	if inToken {
		args = append(args, current.String())
	}
	return args, nil
}
