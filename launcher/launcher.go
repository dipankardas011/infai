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
	args := BuildArgs(engine.Path, m, p)
	return runner.LaunchSpec{Command: args[0], Args: args[1:], Env: engine.Env}, nil
}

func BuildVLLMSpec(engine model.InferenceEngine, m model.ModelEntry, p model.Profile) (runner.LaunchSpec, error) {
	modelPath := strings.TrimSpace(m.ModelPath())
	if modelPath == "" {
		return runner.LaunchSpec{}, fmt.Errorf("vLLM model path is empty")
	}
	if m.Type != "safetensors" && m.Type != "hf_quantized" {
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
		args = append(args, strings.Fields(p.ExtraFlags)...)
	}
	return runner.LaunchSpec{Command: engine.Path, Args: args, Env: engine.Env}, nil
}

func BuildArgs(serverBin string, m model.ModelEntry, p model.Profile) []string {
	args := []string{serverBin, "-m", m.GGUFPath}

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
		for _, f := range strings.Fields(p.ExtraFlags) {
			if f == "--metrics" {
				continue
			}
			args = append(args, f)
		}
	}

	return args
}

func BuildCommand(serverBin string, m model.ModelEntry, p model.Profile) string {
	args := BuildArgs(serverBin, m, p)
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
