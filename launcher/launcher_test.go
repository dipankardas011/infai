package launcher

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dipankardas011/infai/model"
)

func TestBuildSpecVLLM(t *testing.T) {
	gpu := 0.85
	seqs := 32
	batched := 4096
	raw, err := json.Marshal(model.VLLMConfig{
		ServedModelName: "qwen2.5-coder", GPUUtilization: &gpu,
		MaxNumSeqs: &seqs, MaxBatchedTokens: &batched,
		DType: "auto", EnablePrefixCaching: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := model.InferenceEngine{
		Kind: model.EngineVLLM, Path: "/home/user/.local/bin/vllm",
		BaseArgs: []string{"serve"},
		Env:      map[string]string{"FLASHINFER_EXTRA_CUDAFLAGS": "-allow-unsupported-compiler"},
	}
	m := model.ModelEntry{ModelDir: "/models/Qwen2.5-Coder-1.5B", Type: model.TypeSafetensors}
	p := model.Profile{Host: "0.0.0.0", Port: 8000, ContextSize: 8192, EngineConfig: string(raw)}

	spec, err := BuildSpec(engine, m, p)
	if err != nil {
		t.Fatalf("build vLLM spec: %v", err)
	}
	want := []string{
		"serve", "/models/Qwen2.5-Coder-1.5B",
		"--host", "0.0.0.0", "--port", "8000", "--max-model-len", "8192",
		"--gpu-memory-utilization", "0.85", "--max-num-seqs", "32",
		"--max-num-batched-tokens", "4096", "--dtype", "auto",
		"--enable-prefix-caching", "--served-model-name", "qwen2.5-coder",
	}
	if spec.Command != engine.Path || !reflect.DeepEqual(spec.Args, want) {
		t.Fatalf("unexpected spec: %#v", spec)
	}
	if spec.Env["FLASHINFER_EXTRA_CUDAFLAGS"] != "-allow-unsupported-compiler" {
		t.Fatalf("environment not preserved: %#v", spec.Env)
	}
}

func TestBuildSpecRejectsGGUFForVLLM(t *testing.T) {
	for _, modelType := range []model.ModelType{model.TypeGGUF, model.TypeGGUFMultimodal, "mlx", "mlx_quantized"} {
		t.Run(string(modelType), func(t *testing.T) {
			_, err := BuildSpec(
				model.InferenceEngine{Kind: model.EngineVLLM, Path: "vllm"},
				model.ModelEntry{ModelDir: "/models/qwen", Type: modelType},
				model.Profile{},
			)
			if err == nil {
				t.Fatalf("expected %s compatibility error", modelType)
			}
		})
	}
}

func TestBuildSpecLlamaCPP(t *testing.T) {
	engine := model.InferenceEngine{Kind: model.EngineLlamaCPP, Path: "/bin/llama-server"}
	m := model.ModelEntry{ModelDir: "/models", PrimaryFile: "qwen.gguf", Type: model.TypeGGUF}
	p := model.Profile{Host: "127.0.0.1", Port: 8000, ContextSize: 4096, NGL: "auto"}
	spec, err := BuildSpec(engine, m, p)
	if err != nil {
		t.Fatalf("build llama.cpp spec: %v", err)
	}
	if spec.Command != engine.Path || len(spec.Args) == 0 || spec.Args[0] != "-m" {
		t.Fatalf("unexpected spec: %#v", spec)
	}
	if spec.Args[1] != "/models/qwen.gguf" {
		t.Fatalf("expected model path /models/qwen.gguf, got %s", spec.Args[1])
	}
}
