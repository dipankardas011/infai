package launcher

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dipankardas011/infai/internal/model"
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
	for _, modelType := range []model.ModelType{model.TypeGGUF, model.TypeGGUFMultimodal} {
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

func TestBuildSpecRejectsSafetensorsForLlamaCPP(t *testing.T) {
	for _, modelType := range []model.ModelType{model.TypeSafetensors, model.TypeHFQuantized} {
		t.Run(string(modelType), func(t *testing.T) {
			_, err := BuildSpec(
				model.InferenceEngine{Kind: model.EngineLlamaCPP, Path: "/bin/llama-server"},
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
	engine := model.InferenceEngine{Kind: model.EngineLlamaCPP, Path: "/bin/llama-server", BaseArgs: []string{"--no-mmap"}}
	m := model.ModelEntry{ModelDir: "/models", PrimaryFile: "qwen.gguf", Type: model.TypeGGUF}
	p := model.Profile{Host: "127.0.0.1", Port: 8000, ContextSize: 4096, NGL: "auto"}
	spec, err := BuildSpec(engine, m, p)
	if err != nil {
		t.Fatalf("build llama.cpp spec: %v", err)
	}
	if spec.Command != engine.Path || len(spec.Args) < 3 {
		t.Fatalf("unexpected spec: %#v", spec)
	}
	if spec.Args[0] != "--no-mmap" || spec.Args[1] != "-m" {
		t.Fatalf("base arguments were not preserved: %#v", spec.Args)
	}
	if spec.Args[2] != "/models/qwen.gguf" {
		t.Fatalf("expected model path /models/qwen.gguf, got %s", spec.Args[2])
	}
}

func TestParseExtraFlagsPreservesQuotedValues(t *testing.T) {
	args, err := ParseExtraFlags(`--foo "hello world" --bar='two words'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--foo", "hello world", "--bar=two words"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("got %#v, want %#v", args, want)
	}
}

func TestBuildSpecRejectsManagedExtraFlags(t *testing.T) {
	_, err := BuildSpec(
		model.InferenceEngine{Kind: model.EngineLlamaCPP, Path: "/bin/llama-server"},
		model.ModelEntry{ModelDir: "/models", PrimaryFile: "qwen.gguf", Type: model.TypeGGUF},
		model.Profile{Host: "127.0.0.1", Port: 8000, ContextSize: 4096, NGL: "auto", ExtraFlags: `--host "0.0.0.0"`},
	)
	if err == nil || !strings.Contains(err.Error(), "managed option") {
		t.Fatalf("expected managed flag conflict, got %v", err)
	}
}

func TestBuildLlamaCPPSpecSpeculativeArgs(t *testing.T) {
	tokens := 5
	engine := model.InferenceEngine{Kind: model.EngineLlamaCPP, Path: "/bin/llama-server"}
	target := model.ModelEntry{ModelDir: "/models/target", PrimaryFile: "target.gguf", Type: model.TypeGGUF}
	draft := model.ModelEntry{ModelDir: "/models/draft", PrimaryFile: "draft.gguf", Type: model.TypeGGUF}
	base := model.Profile{Host: "127.0.0.1", Port: 8080, ContextSize: 4096, NGL: "auto", SpeculativeTokens: &tokens}

	t.Run("native MTP", func(t *testing.T) {
		p := base
		p.SpeculativeMode = model.SpeculativeNativeMTP
		spec, err := BuildLlamaCPPSpec(engine, target, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"-m", "/models/target/target.gguf", "--port", "8080", "--host", "127.0.0.1", "-c", "4096", "-ngl", "auto", "--metrics", "--spec-type", "draft-mtp", "--spec-draft-n-max", "5"}
		if !reflect.DeepEqual(spec.Args, want) {
			t.Fatalf("args = %#v, want %#v", spec.Args, want)
		}
	})

	t.Run("draft model", func(t *testing.T) {
		p := base
		p.SpeculativeMode = model.SpeculativeDraftModel
		spec, err := BuildLlamaCPPSpec(engine, target, p, &draft)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"-m", "/models/target/target.gguf", "--port", "8080", "--host", "127.0.0.1", "-c", "4096", "-ngl", "auto", "--metrics", "--spec-type", "draft-simple", "--spec-draft-model", "/models/draft/draft.gguf", "--spec-draft-n-max", "5"}
		if !reflect.DeepEqual(spec.Args, want) {
			t.Fatalf("args = %#v, want %#v", spec.Args, want)
		}
	})

	t.Run("MTP assistant", func(t *testing.T) {
		p := base
		p.SpeculativeMode = model.SpeculativeMTPAssistant
		spec, err := BuildLlamaCPPSpec(engine, target, p, &draft)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"-m", "/models/target/target.gguf", "--port", "8080", "--host", "127.0.0.1", "-c", "4096", "-ngl", "auto", "--metrics", "--spec-type", "draft-mtp", "--spec-draft-model", "/models/draft/draft.gguf", "--spec-draft-n-max", "5"}
		if !reflect.DeepEqual(spec.Args, want) {
			t.Fatalf("args = %#v, want %#v", spec.Args, want)
		}
	})
}

func TestBuildVLLMSpecSpeculativeConfig(t *testing.T) {
	tokens := 3
	engine := model.InferenceEngine{Kind: model.EngineVLLM, Path: "vllm"}
	target := model.ModelEntry{ModelDir: "/models/target", Type: model.TypeSafetensors}
	draft := model.ModelEntry{ModelDir: "/models/draft", Type: model.TypeHFQuantized}

	tests := []struct {
		name  string
		mode  model.SpeculativeMode
		draft *model.ModelEntry
		json  string
	}{
		{name: "native MTP", mode: model.SpeculativeNativeMTP, json: `{"method":"mtp","num_speculative_tokens":3}`},
		{name: "draft model", mode: model.SpeculativeDraftModel, draft: &draft, json: `{"method":"draft_model","model":"/models/draft","num_speculative_tokens":3}`},
		{name: "MTP assistant", mode: model.SpeculativeMTPAssistant, draft: &draft, json: `{"method":"mtp","model":"/models/draft","num_speculative_tokens":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := model.Profile{Host: "0.0.0.0", Port: 8000, ContextSize: 8192, SpeculativeMode: tt.mode, SpeculativeTokens: &tokens}
			spec, err := BuildVLLMSpec(engine, target, p, tt.draft)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"serve", "/models/target", "--host", "0.0.0.0", "--port", "8000", "--max-model-len", "8192", "--speculative-config", tt.json}
			if !reflect.DeepEqual(spec.Args, want) {
				t.Fatalf("args = %#v, want %#v", spec.Args, want)
			}
		})
	}
}

func TestBuildSpecRejectsManagedSpeculativeFlags(t *testing.T) {
	tests := []struct {
		name   string
		engine model.InferenceEngine
		entry  model.ModelEntry
		flags  string
	}{
		{name: "llama", engine: model.InferenceEngine{Kind: model.EngineLlamaCPP, Path: "llama-server"}, entry: model.ModelEntry{ModelDir: "/models", PrimaryFile: "m.gguf", Type: model.TypeGGUF}, flags: "--spec-type=draft-mtp"},
		{name: "vllm", engine: model.InferenceEngine{Kind: model.EngineVLLM, Path: "vllm"}, entry: model.ModelEntry{ModelDir: "/models/m", Type: model.TypeSafetensors}, flags: `--speculative-config '{}'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildSpec(tt.engine, tt.entry, model.Profile{Host: "127.0.0.1", Port: 8000, ContextSize: 4096, NGL: "auto", ExtraFlags: tt.flags})
			if err == nil || !strings.Contains(err.Error(), "managed option") {
				t.Fatalf("expected managed flag error, got %v", err)
			}
		})
	}
}
