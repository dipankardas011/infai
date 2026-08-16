package backend

import (
	"fmt"
	"sort"

	"github.com/dipankardas011/infai/model"
)

// OptionCategory controls where an option belongs in the profile editor.
type OptionCategory string

const (
	CategoryCommon   OptionCategory = "common"
	CategoryAdvanced OptionCategory = "advanced"
	CategorySecurity OptionCategory = "security"
)

// OptionValueKind describes the value expected by the editor and validator.
type OptionValueKind string

const (
	ValueInteger OptionValueKind = "integer"
	ValueFloat   OptionValueKind = "float"
	ValueString  OptionValueKind = "string"
	ValueBoolean OptionValueKind = "boolean"
	ValueChoice  OptionValueKind = "choice"
)

// Option is the single source of metadata for a curated profile setting.
// ProfileField or EngineConfigField identifies the persisted representation;
// options not yet represented by the current profile model are marked as
// Planned so later schema/launcher work can add them deliberately.
type Option struct {
	Key               string
	Engine            model.EngineKind
	Category          OptionCategory
	ValueKind         OptionValueKind
	ProfileField      string
	EngineConfigField string
	CLIFlag           string
	Choices           []string
	Default           string
	OmitWhenUnset     bool
	Description       string
	MemoryImpact      string
	QualityImpact     string
	SecurityCaution   string
	Capability        string
	Planned           bool
}

var optionCatalog = []Option{
	{Key: "host", Category: CategoryCommon, ValueKind: ValueString, ProfileField: "Host", CLIFlag: "--host", Default: "0.0.0.0", Description: "Network address where the inference server listens.", SecurityCaution: "Binding beyond localhost can expose the server to the network."},
	{Key: "port", Category: CategoryCommon, ValueKind: ValueInteger, ProfileField: "Port", CLIFlag: "--port", Default: "8000", Description: "TCP port used by the inference server."},
	{Key: "context", Category: CategoryCommon, ValueKind: ValueInteger, ProfileField: "ContextSize", CLIFlag: "--max-model-len / -c", Description: "Maximum number of tokens retained in the prompt context.", MemoryImpact: "Larger contexts require more KV-cache memory."},
	{Key: "extra_flags", Category: CategoryAdvanced, ValueKind: ValueString, ProfileField: "ExtraFlags", Description: "Escape hatch for executor arguments not represented by the catalog.", SecurityCaution: "Arguments are passed to the inference process and should be reviewed before launch."},

	{Key: "gpu_layers", Engine: model.EngineLlamaCPP, Category: CategoryCommon, ValueKind: ValueString, ProfileField: "NGL", CLIFlag: "-ngl", Default: "auto", Description: "Number of model layers to offload to the accelerator.", MemoryImpact: "More offloaded layers consume accelerator memory; auto lets llama.cpp choose."},
	{Key: "batch", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueInteger, ProfileField: "BatchSize", CLIFlag: "-b", OmitWhenUnset: true, Description: "Logical prompt-processing batch size.", MemoryImpact: "Larger batches require more working memory."},
	{Key: "ubatch", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueInteger, ProfileField: "UBatchSize", CLIFlag: "-ub", OmitWhenUnset: true, Description: "Physical micro-batch size used during prompt processing.", MemoryImpact: "Larger micro-batches require more working memory."},
	{Key: "cache_type_k", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueChoice, ProfileField: "CacheTypeK", CLIFlag: "--cache-type-k", Choices: []string{"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"}, OmitWhenUnset: true, Description: "Data type used for the key portion of the KV cache.", MemoryImpact: "Quantized types reduce KV memory at a possible quality cost."},
	{Key: "cache_type_v", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueChoice, ProfileField: "CacheTypeV", CLIFlag: "--cache-type-v", Choices: []string{"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"}, OmitWhenUnset: true, Description: "Data type used for the value portion of the KV cache.", MemoryImpact: "Quantized types reduce KV memory at a possible quality cost."},
	{Key: "flash_attention", Engine: model.EngineLlamaCPP, Category: CategoryCommon, ValueKind: ValueBoolean, ProfileField: "FlashAttn", CLIFlag: "--flash-attn", Description: "Enable flash attention where supported by the build and hardware."},
	{Key: "jinja", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueBoolean, ProfileField: "Jinja", CLIFlag: "--jinja", Description: "Use the model's Jinja chat template."},
	{Key: "temperature", Engine: model.EngineLlamaCPP, Category: CategoryCommon, ValueKind: ValueFloat, ProfileField: "Temperature", CLIFlag: "--temperature", OmitWhenUnset: true, Description: "Sampling temperature controlling output randomness."},
	{Key: "reasoning_budget", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueInteger, ProfileField: "ReasoningBudget", CLIFlag: "--reasoning-budget", OmitWhenUnset: true, Description: "Token budget for supported reasoning models."},
	{Key: "top_p", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueFloat, ProfileField: "TopP", CLIFlag: "--top_p", OmitWhenUnset: true, Description: "Nucleus sampling probability cutoff."},
	{Key: "top_k", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueInteger, ProfileField: "TopK", CLIFlag: "--top_k", OmitWhenUnset: true, Description: "Maximum number of candidate tokens considered during sampling."},
	{Key: "kv_offload", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueBoolean, ProfileField: "NoKVOffload", CLIFlag: "--no-kv-offload", Description: "Keep the KV cache in system memory instead of offloading it."},
	{Key: "mmproj", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueBoolean, ProfileField: "UseMmproj", CLIFlag: "--mmproj", Description: "Load the detected multimodal projector."},
	{Key: "cpu_threads", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueInteger, CLIFlag: "--threads", Description: "CPU threads used by llama.cpp.", Planned: true},
	{Key: "parallel_slots", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueInteger, CLIFlag: "-np", Description: "Number of parallel sequence slots.", Planned: true},
	{Key: "prefix_caching", Engine: model.EngineLlamaCPP, Category: CategoryAdvanced, ValueKind: ValueBoolean, CLIFlag: "--cache-prompt", Description: "Reuse cached prompt prefixes when supported.", Planned: true},

	{Key: "gpu_memory_utilization", Engine: model.EngineVLLM, Category: CategoryCommon, ValueKind: ValueFloat, EngineConfigField: "GPUUtilization", CLIFlag: "--gpu-memory-utilization", Default: "0.85", Description: "Fraction of GPU memory vLLM may use.", MemoryImpact: "Higher values leave less runtime headroom."},
	{Key: "max_num_seqs", Engine: model.EngineVLLM, Category: CategoryCommon, ValueKind: ValueInteger, EngineConfigField: "MaxNumSeqs", CLIFlag: "--max-num-seqs", Default: "8", Description: "Maximum number of sequences processed concurrently.", MemoryImpact: "More concurrent sequences increase KV-cache demand."},
	{Key: "max_num_batched_tokens", Engine: model.EngineVLLM, Category: CategoryAdvanced, ValueKind: ValueInteger, EngineConfigField: "MaxBatchedTokens", CLIFlag: "--max-num-batched-tokens", Default: "4096", Description: "Maximum tokens scheduled in one batch."},
	{Key: "dtype", Engine: model.EngineVLLM, Category: CategoryCommon, ValueKind: ValueChoice, EngineConfigField: "DType", CLIFlag: "--dtype", Choices: []string{"auto", "float16", "bfloat16", "float32"}, Default: "auto", Description: "Data type used for model execution."},
	{Key: "tensor_parallel_size", Engine: model.EngineVLLM, Category: CategoryAdvanced, ValueKind: ValueInteger, EngineConfigField: "TensorParallelSize", CLIFlag: "--tensor-parallel-size", Description: "Number of accelerator workers used for tensor parallelism."},
	{Key: "pipeline_parallel_size", Engine: model.EngineVLLM, Category: CategoryAdvanced, ValueKind: ValueInteger, EngineConfigField: "PipelineParallelSize", CLIFlag: "--pipeline-parallel-size", Description: "Number of pipeline stages used for model execution."},
	{Key: "kv_cache_dtype", Engine: model.EngineVLLM, Category: CategoryAdvanced, ValueKind: ValueChoice, CLIFlag: "--kv-cache-dtype", Description: "Data type used for vLLM's KV cache.", Planned: true},
	{Key: "prefix_caching", Engine: model.EngineVLLM, Category: CategoryAdvanced, ValueKind: ValueBoolean, EngineConfigField: "EnablePrefixCaching", CLIFlag: "--enable-prefix-caching", Description: "Reuse cached prompt prefixes between requests."},
	{Key: "served_model_name", Engine: model.EngineVLLM, Category: CategoryCommon, ValueKind: ValueString, EngineConfigField: "ServedModelName", CLIFlag: "--served-model-name", OmitWhenUnset: true, Description: "Name presented by the OpenAI-compatible API."},
	{Key: "trust_remote_code", Engine: model.EngineVLLM, Category: CategorySecurity, ValueKind: ValueBoolean, EngineConfigField: "TrustRemoteCode", CLIFlag: "--trust-remote-code", Description: "Allow model repositories to execute custom Python code.", SecurityCaution: "Only enable this for repositories you trust."},
}

// OptionCatalog returns a copy so callers cannot mutate the shared catalog.
func OptionCatalog() []Option {
	out := make([]Option, len(optionCatalog))
	copy(out, optionCatalog)
	for i := range out {
		out[i].Choices = append([]string(nil), out[i].Choices...)
	}
	return out
}

// OptionsFor returns common options and options belonging to the selected engine.
func OptionsFor(kind model.EngineKind) []Option {
	if kind == "" {
		kind = model.EngineLlamaCPP
	}
	out := make([]Option, 0)
	for _, option := range optionCatalog {
		if option.Engine == "" || option.Engine == kind {
			copy := option
			copy.Choices = append([]string(nil), option.Choices...)
			out = append(out, copy)
		}
	}
	return out
}

// ValidateOptionCatalog checks the invariants needed by later consumers.
func ValidateOptionCatalog() error {
	seen := make(map[string]bool, len(optionCatalog))
	for _, option := range optionCatalog {
		key := string(option.Engine) + "\x00" + option.Key
		if option.Key == "" || seen[key] {
			return fmt.Errorf("duplicate or empty option key %q for engine %q", option.Key, option.Engine)
		}
		seen[key] = true
		if option.Category != CategoryCommon && option.Category != CategoryAdvanced && option.Category != CategorySecurity {
			return fmt.Errorf("option %q has invalid category %q", option.Key, option.Category)
		}
		if option.ValueKind == "" || option.Description == "" {
			return fmt.Errorf("option %q is missing value kind or description", option.Key)
		}
		if option.ProfileField == "" && option.EngineConfigField == "" && !option.Planned && option.Key != "extra_flags" {
			return fmt.Errorf("option %q has no persistence mapping", option.Key)
		}
	}
	return nil
}

// OptionKeys returns stable keys for diagnostics and tests.
func OptionKeys(kind model.EngineKind) []string {
	options := OptionsFor(kind)
	keys := make([]string, 0, len(options))
	for _, option := range options {
		keys = append(keys, option.Key)
	}
	sort.Strings(keys)
	return keys
}
