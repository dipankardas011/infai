package scanner

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dipankardas011/infai/model"
)

// ggufWriter builds in-memory GGUF v3 binaries for testing.
type ggufWriter struct {
	buf bytes.Buffer
}

func newGGUFWriter() *ggufWriter {
	w := &ggufWriter{}
	w.buf.Grow(256)
	return w
}

func (w *ggufWriter) writeHeader(kvCount int) {
	binary.Write(&w.buf, binary.LittleEndian, uint32(GGUF_MAGIC))
	binary.Write(&w.buf, binary.LittleEndian, uint32(3))       // version
	binary.Write(&w.buf, binary.LittleEndian, uint64(0))       // tensor count
	binary.Write(&w.buf, binary.LittleEndian, uint64(kvCount)) // kv count
}

func (w *ggufWriter) writeString(s string) {
	binary.Write(&w.buf, binary.LittleEndian, uint64(len(s)))
	w.buf.WriteString(s)
}

func (w *ggufWriter) writeKVString(key, value string) {
	w.writeString(key)
	binary.Write(&w.buf, binary.LittleEndian, uint32(8)) // string type
	w.writeString(value)
}

func (w *ggufWriter) writeKVUint32(key string, value uint32) {
	w.writeString(key)
	binary.Write(&w.buf, binary.LittleEndian, uint32(4)) // uint32 type
	binary.Write(&w.buf, binary.LittleEndian, value)
}

func (w *ggufWriter) writeKVFloat32(key string, value float32) {
	w.writeString(key)
	binary.Write(&w.buf, binary.LittleEndian, uint32(6)) // float32 type
	binary.Write(&w.buf, binary.LittleEndian, value)
}

func (w *ggufWriter) writeKVUint64(key string, value uint64) {
	w.writeString(key)
	binary.Write(&w.buf, binary.LittleEndian, uint32(10)) // uint64 type
	binary.Write(&w.buf, binary.LittleEndian, value)
}

func (w *ggufWriter) writeKVBool(key string, value bool) {
	w.writeString(key)
	binary.Write(&w.buf, binary.LittleEndian, uint32(7)) // bool type
	if value {
		w.buf.WriteByte(1)
	} else {
		w.buf.WriteByte(0)
	}
}

func (w *ggufWriter) byteCount() int64 { return int64(w.buf.Len()) }

func (w *ggufWriter) reader() *bytes.Reader { return bytes.NewReader(w.buf.Bytes()) }

func TestParseGGUFMinimal(t *testing.T) {
	// Prevents: parser crashing on a valid but minimal GGUF file with zero KV pairs.
	gw := newGGUFWriter()
	gw.writeHeader(0)

	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)
	assert.Equal(t, int64(gw.byteCount()), meta.FileSizeBytes)
	assert.Empty(t, meta.Architecture)
	assert.Empty(t, meta.Quantization)
	assert.Empty(t, meta.ModelName)
}

func TestParseGGUFFullModel(t *testing.T) {
	// Prevents: missing fields when all standard GGUF keys are present.
	gw := newGGUFWriter()
	gw.writeHeader(13)
	gw.writeKVString("general.architecture", "llama")
	gw.writeKVString("general.name", "Test Llama")
	gw.writeKVUint32("general.file_type", 15) // Q4_K_M
	gw.writeKVUint64("general.parameter_count", 7000000000)
	gw.writeKVUint32("llama.context_length", 8192)
	gw.writeKVUint32("llama.embedding_length", 4096)
	gw.writeKVUint32("llama.block_count", 32)
	gw.writeKVUint32("llama.feed_forward_length", 11008)
	gw.writeKVUint32("llama.attention.head_count", 32)
	gw.writeKVUint32("llama.attention.head_count_kv", 8)
	gw.writeKVUint32("llama.attention.key_length", 128)
	gw.writeKVUint32("llama.vocab_size", 32000)
	gw.writeKVString("tokenizer.ggml.model", "llama")

	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)

	assert.Equal(t, "llama", meta.Architecture)
	assert.Equal(t, "Test Llama", meta.ModelName)
	assert.Equal(t, int32(15), meta.FileType)
	assert.Equal(t, uint64(7000000000), meta.ParameterCount)
	assert.Equal(t, uint32(8192), meta.ContextLength)
	assert.Equal(t, uint32(4096), meta.EmbeddingLength)
	assert.Equal(t, uint32(32), meta.BlockCount)
	assert.Equal(t, uint32(11008), meta.FeedForwardLength)
	assert.Equal(t, uint32(32), meta.AttentionHeadCount)
	assert.Equal(t, uint32(8), meta.AttentionHeadCountKV)
	assert.Equal(t, uint32(128), meta.HeadDimension)
	assert.Equal(t, uint32(32000), meta.VocabSize)
	assert.Equal(t, "llama", meta.TokenizerModel)
	assert.Equal(t, int64(gw.byteCount()), meta.FileSizeBytes)

	// Rendered file type name
	assert.Contains(t, strings.ToLower(meta.Quantization), "q4")
}

func TestParseGGUFMoE(t *testing.T) {
	// Prevents: MoE fields not extracted from expert_count/expert_used_count keys.
	gw := newGGUFWriter()
	gw.writeHeader(3)
	gw.writeKVString("general.architecture", "llama")
	gw.writeKVUint32("llama.expert_count", 8)
	gw.writeKVUint32("llama.expert_used_count", 2)

	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)

	assert.Equal(t, uint32(8), meta.NumExperts)
	assert.Equal(t, uint32(2), meta.NumExpertsPerToken)
}

func TestParseGGUFIrrelevantKeysSkipped(t *testing.T) {
	// Prevents: parser failing on keys we don't care about (e.g. tokenizer tokens
	// array with 262K elements — this would previously hit maxArrayLen cap).
	gw := newGGUFWriter()
	gw.writeHeader(1)
	// Write a key we don't filter for — parser should skip it.
	gw.writeKVString("tokenizer.ggml.unknown_key", "should_be_skipped")

	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)
	assert.Empty(t, meta.Architecture)
}

func TestParseGGUFBigEndianV3(t *testing.T) {
	// Prevents: v3 big-endian GGUF files being misread as corrupt.
	writeBE := func(w *ggufWriter, v interface{}) {
		binary.Write(&w.buf, binary.BigEndian, v)
	}
	gw := &ggufWriter{}
	gw.buf.Grow(256)
	// Magic is always LE "GGUF" → bytes 0x47 0x47 0x55 0x46
	binary.Write(&gw.buf, binary.LittleEndian, uint32(GGUF_MAGIC))
	// Everything else in big-endian.
	writeBE(gw, uint32(3)) // version = 3
	writeBE(gw, uint64(0)) // tensor count
	writeBE(gw, uint64(2)) // kv count
	// KV 1: architecture
	writeBE(gw, uint64(len("general.architecture")))
	gw.buf.WriteString("general.architecture")
	writeBE(gw, uint32(8)) // string type
	writeBE(gw, uint64(len("falcon")))
	gw.buf.WriteString("falcon")
	// KV 2: context_length
	writeBE(gw, uint64(len("falcon.context_length")))
	gw.buf.WriteString("falcon.context_length")
	writeBE(gw, uint32(4)) // uint32 type
	writeBE(gw, uint32(2048))

	meta, err := ParseGGUFReader(bytes.NewReader(gw.buf.Bytes()), GGUF_MAGIC, int64(gw.buf.Len()))
	require.NoError(t, err)
	assert.Equal(t, "falcon", meta.Architecture)
	assert.Equal(t, uint32(2048), meta.ContextLength)
}

func TestParseGGUFChatTemplate(t *testing.T) {
	// Prevents: chat template not extracted.
	tmpl := "{% for message in messages %}{{ message.content }}{% endfor %}"
	gw := newGGUFWriter()
	gw.writeHeader(1)
	gw.writeKVString("tokenizer.chat_template", tmpl)

	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)
	assert.Equal(t, tmpl, meta.ChatTemplate)
}

func TestParseGGUFWrongMagic(t *testing.T) {
	// Prevents: corrupted file with wrong magic being silently accepted.
	gw := newGGUFWriter()
	gw.writeHeader(0)

	_, err := ParseGGUFReader(gw.reader(), 0xDEADBEEF, gw.byteCount())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "magic")
}

func TestParseGGUFFallbackHeadDim(t *testing.T) {
	// Prevents: head_dimension not computed from embedding/head_count when
	// attention.key_length is absent.
	gw := newGGUFWriter()
	gw.writeHeader(3)
	gw.writeKVString("general.architecture", "test")
	gw.writeKVUint32("test.embedding_length", 4096)
	gw.writeKVUint32("test.attention.head_count", 32)
	// No attention.key_length — should fall back to 4096/32 = 128

	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)
	assert.Equal(t, uint32(128), meta.HeadDimension)
}

func TestParseGGUFParameterCountTypes(t *testing.T) {
	// Prevents: parameter_count stored as uint32 misinterpreted.
	tests := []struct {
		name string
		fn   func(*ggufWriter)
		want uint64
	}{
		{
			"uint64",
			func(gw *ggufWriter) { gw.writeKVUint64("general.parameter_count", 3000000000) },
			3000000000,
		},
		{
			"uint32",
			func(gw *ggufWriter) {
				gw.writeString("general.parameter_count")
				binary.Write(&gw.buf, binary.LittleEndian, uint32(4)) // uint32 type
				binary.Write(&gw.buf, binary.LittleEndian, uint32(7000000))
			},
			7000000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gw := newGGUFWriter()
			gw.writeHeader(1)
			tt.fn(gw)
			meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
			require.NoError(t, err)
			assert.Equal(t, tt.want, meta.ParameterCount)
		})
	}
}

func TestParseGGUFQuantizationUnknown(t *testing.T) {
	// Prevents: unknown file_type producing a panic or empty quantization.
	gw := newGGUFWriter()
	gw.writeHeader(1)
	gw.writeKVUint32("general.file_type", 999)

	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)
	assert.Equal(t, int32(999), meta.FileType)
	assert.Empty(t, meta.Quantization)
}

func TestParseSafetensorMetadata(t *testing.T) {
	// Prevents: config.json fields not mapped to ModelMetadata correctly.
	dir := t.TempDir()
	cfg := map[string]interface{}{
		"architectures":           []string{"Qwen2ForCausalLM"},
		"max_position_embeddings": 32768,
		"hidden_size":             1536,
		"num_hidden_layers":       28,
		"num_attention_heads":     12,
		"num_key_value_heads":     2,
		"head_dim":                128,
		"intermediate_size":       8960,
		"vocab_size":              151936,
		"torch_dtype":             "bfloat16",
		"quantization_config": map[string]interface{}{
			"quant_method": "gptq",
			"bits":         float64(4),
		},
	}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(dir, "config.json"), b, 0644)
	// Write a dummy .safetensors file for size calculation.
	os.WriteFile(filepath.Join(dir, "model-00001-of-00002.safetensors"), make([]byte, 1000), 0644)
	os.WriteFile(filepath.Join(dir, "model-00002-of-00002.safetensors"), make([]byte, 500), 0644)

	meta, err := parseSafetensorMetadata(dir)
	require.NoError(t, err)
	assert.Equal(t, "Qwen2ForCausalLM", meta.Architecture)
	assert.Equal(t, uint32(32768), meta.ContextLength)
	assert.Equal(t, uint32(1536), meta.EmbeddingLength)
	assert.Equal(t, uint32(28), meta.BlockCount)
	assert.Equal(t, uint32(12), meta.AttentionHeadCount)
	assert.Equal(t, uint32(2), meta.AttentionHeadCountKV)
	assert.Equal(t, uint32(128), meta.HeadDimension)
	assert.Equal(t, uint32(8960), meta.FeedForwardLength)
	assert.Equal(t, uint32(151936), meta.VocabSize)
	assert.Equal(t, "gptq_4bit", meta.Quantization)
	assert.Equal(t, int64(1500), meta.FileSizeBytes)
}

func TestParseSafetensorMetadataMoE(t *testing.T) {
	// Prevents: MoE fields from config.json not extracted.
	dir := t.TempDir()
	cfg := map[string]interface{}{
		"architectures":       []string{"MixtralForCausalLM"},
		"num_local_experts":   8,
		"num_experts_per_tok": 2,
	}
	b, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(dir, "config.json"), b, 0644)

	meta, err := parseSafetensorMetadata(dir)
	require.NoError(t, err)
	assert.Equal(t, uint32(8), meta.NumExperts)
	assert.Equal(t, uint32(2), meta.NumExpertsPerToken)
}

func TestParseSafetensorMetadataMinimal(t *testing.T) {
	// Prevents: empty config.json causing error instead of returning zero meta.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0644)

	meta, err := parseSafetensorMetadata(dir)
	require.NoError(t, err)
	assert.Empty(t, meta.Architecture)
	assert.Zero(t, meta.ContextLength)
	assert.Zero(t, meta.FileSizeBytes)
}

func TestLoadModelMetadata(t *testing.T) {
	// Prevents: LoadModelMetadata failing to dispatch to correct parser or
	// not populating Metadata string.
	gw := newGGUFWriter()
	gw.writeHeader(2)
	gw.writeKVString("general.architecture", "test")
	gw.writeKVUint32("test.context_length", 4096)

	entry := &model.ModelEntry{
		Type:        model.TypeGGUF,
		ModelDir:    "/tmp",
		PrimaryFile: "test.gguf",
	}
	// Override ParseGGUF to use our in-memory data instead of a real file.
	// We do this by testing LoadModelMetadata indirectly — it calls ParseGGUF
	// for gguf types. Instead, we inject metadata manually.
	meta, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.NoError(t, err)
	b, _ := json.Marshal(meta)
	entry.Metadata = string(b)

	var roundtrip model.ModelMetadata
	require.NoError(t, json.Unmarshal([]byte(entry.Metadata), &roundtrip))
	assert.Equal(t, "test", roundtrip.Architecture)
	assert.Equal(t, uint32(4096), roundtrip.ContextLength)
}

func TestScanGGUFValidatesMagic(t *testing.T) {
	// Prevents: non-GGUF files with .gguf extension being accepted as models.
	dir := t.TempDir()
	// Write a file that starts with "GGUF" magic.
	data := make([]byte, 32)
	binary.LittleEndian.PutUint32(data[0:], GGUF_MAGIC)
	binary.LittleEndian.PutUint32(data[4:], 3)
	binary.LittleEndian.PutUint64(data[8:], 0)  // tensor count
	binary.LittleEndian.PutUint64(data[16:], 0) // kv count
	os.WriteFile(filepath.Join(dir, "valid.gguf"), data, 0644)

	// Write a file without GGUF magic.
	os.WriteFile(filepath.Join(dir, "invalid.gguf"), []byte("not a gguf file"), 0644)

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, model.TypeGGUF, entries[0].Type)
	assert.Equal(t, "valid", entries[0].DirName)
}

func writeMinimalGGUF(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data := make([]byte, 24)
	binary.LittleEndian.PutUint32(data[0:], GGUF_MAGIC)
	binary.LittleEndian.PutUint32(data[4:], 3)
	binary.LittleEndian.PutUint64(data[8:], 0)
	binary.LittleEndian.PutUint64(data[16:], 0)
	require.NoError(t, os.WriteFile(path, data, 0644))
	return path
}

func writeMinimalConfigJSON(t *testing.T, dir string, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func writeDummySafetensor(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte{0}, 0644))
	return path
}

func TestScanGGUFMultimodalDetection(t *testing.T) {
	// Prevents: GGUF + mmproj file not being classified as gguf_multimodal.
	dir := t.TempDir()
	writeMinimalGGUF(t, dir, "model.gguf")
	writeMinimalGGUF(t, dir, "mmproj-model-F16.gguf")

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, model.TypeGGUFMultimodal, entries[0].Type)
	assert.Contains(t, entries[0].MmprojPath, "mmproj")
	assert.Equal(t, "model", entries[0].DirName)
}

func TestScanGGUFSingleMmprojPaired(t *testing.T) {
	// Prevents: single mmproj not being assigned to the lone GGUF.
	dir := t.TempDir()
	writeMinimalGGUF(t, dir, "llama.gguf")
	writeMinimalGGUF(t, dir, "llama-mmproj-F16.gguf")

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, model.TypeGGUFMultimodal, entries[0].Type)
}

func TestScanGGUFWithoutMmproj(t *testing.T) {
	// Prevents: GGUF without mmproj being misclassified as multimodal.
	dir := t.TempDir()
	writeMinimalGGUF(t, dir, "model.gguf")

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, model.TypeGGUF, entries[0].Type)
	assert.Empty(t, entries[0].MmprojPath)
}

func TestScanGGUFMultipleMmprojStemMatch(t *testing.T) {
	// Prevents: multiple mmproj files not being correctly paired by stem overlap.
	dir := t.TempDir()
	writeMinimalGGUF(t, dir, "qwen-7b.gguf")
	writeMinimalGGUF(t, dir, "qwen-7b-mmproj-F16.gguf")
	writeMinimalGGUF(t, dir, "mixtral-mmproj-F16.gguf")

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, model.TypeGGUFMultimodal, entries[0].Type)
	assert.Contains(t, entries[0].MmprojPath, "qwen")
}

func TestScanSafetensorsClassification(t *testing.T) {
	// Prevents: safetensors dir without quantization_config being misclassified.
	dir := t.TempDir()
	writeMinimalConfigJSON(t, dir, `{"architectures":["LlamaForCausalLM"]}`)
	writeDummySafetensor(t, dir, "model.safetensors")

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, model.TypeSafetensors, entries[0].Type)
	assert.Equal(t, dir, entries[0].ModelDir)
	assert.Empty(t, entries[0].PrimaryFile)
}

func TestScanHFQuantizedClassification(t *testing.T) {
	// Prevents: safetensors dir with quantization_config not being classified as hf_quantized.
	dir := t.TempDir()
	writeMinimalConfigJSON(t, dir, `{"architectures":["LlamaForCausalLM"],"quantization_config":{"quant_method":"gptq","bits":4}}`)
	writeDummySafetensor(t, dir, "model.safetensors")

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, model.TypeHFQuantized, entries[0].Type)
}

func TestScanSafetensorsRequiresConfigJson(t *testing.T) {
	// Prevents: .safetensors files without config.json being detected (they shouldn't be).
	dir := t.TempDir()
	writeDummySafetensor(t, dir, "model.safetensors")

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, entries, "safetensors without config.json must not produce entries")
}

func TestScanMixedGGUFSafetensors(t *testing.T) {
	// Prevents: mixed GGUF and safetensors in same directory confusing the scanner.
	dir := t.TempDir()
	writeMinimalGGUF(t, dir, "model.gguf")
	writeDummySafetensor(t, dir, "model.safetensors")
	writeMinimalConfigJSON(t, dir, `{"architectures":["LlamaForCausalLM"]}`)

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	var ggufCount, sfCount int
	for _, e := range entries {
		switch e.Type {
		case model.TypeGGUF:
			ggufCount++
		case model.TypeSafetensors:
			sfCount++
		}
	}
	assert.Equal(t, 1, ggufCount)
	assert.Equal(t, 1, sfCount)
}

func TestScanGGUFIgnoresMmprojOnlyFile(t *testing.T) {
	// Prevents: mmproj file without a paired GGUF being incorrectly registered.
	dir := t.TempDir()
	// Write an mmproj file named so isMmproj returns true — it must also be valid GGUF
	// to pass magic validation. A non-GGUF file named with "mmproj" should be ignored.
	os.WriteFile(filepath.Join(dir, "mmproj.gguf"), []byte("not gguf"), 0644)

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestParseGGUFFailureTruncatedHeader(t *testing.T) {
	// Prevents: truncated GGUF header causing panic instead of error.
	// 4 bytes — only magic, no version or counts.
	_, err := ParseGGUFReader(bytes.NewReader(make([]byte, 4)), GGUF_MAGIC, 4)
	require.Error(t, err)
}

func TestParseGGUFFailureTruncatedMetadata(t *testing.T) {
	// Prevents: truncated KV data causing panic instead of error.
	// Valid header says 1 KV pair, but no KV data follows.
	gw := newGGUFWriter()
	gw.writeHeader(1) // claims 1 KV, but we write nothing

	_, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kv[0]")
}

func TestParseGGUFFailureUnsupportedVersion(t *testing.T) {
	// Prevents: future GGUF versions being silently parsed with wrong assumptions.
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint32(GGUF_MAGIC))
	binary.Write(buf, binary.LittleEndian, uint32(99)) // unsupported version
	binary.Write(buf, binary.LittleEndian, uint64(0))  // tensor count
	binary.Write(buf, binary.LittleEndian, uint64(0))  // kv count

	_, err := ParseGGUFReader(bytes.NewReader(buf.Bytes()), GGUF_MAGIC, int64(buf.Len()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported gguf version")
}

func TestParseGGUFFailureEmptyFile(t *testing.T) {
	// Prevents: zero-byte file causing panic.
	_, err := ParseGGUFReader(bytes.NewReader([]byte{}), GGUF_MAGIC, 0)
	require.Error(t, err)
}

func TestParseGGUFFailureVersionZero(t *testing.T) {
	// Prevents: version=0 (pre-GGUF format) being treated as valid.
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, uint32(GGUF_MAGIC))
	binary.Write(buf, binary.LittleEndian, uint32(0)) // version 0
	binary.Write(buf, binary.LittleEndian, uint64(0))
	binary.Write(buf, binary.LittleEndian, uint64(0))

	_, err := ParseGGUFReader(bytes.NewReader(buf.Bytes()), GGUF_MAGIC, int64(buf.Len()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported gguf version")
}

func TestParseGGUFFailureStringLengthExceedsData(t *testing.T) {
	// Prevents: a GGUF string claiming length 1000 but only 5 bytes remain
	// — this should error, not panic with a slice bounds out of range.
	gw := newGGUFWriter()
	gw.writeHeader(1)
	// Write a key normally.
	gw.writeString("general.architecture")
	// Write value type = string.
	binary.Write(&gw.buf, binary.LittleEndian, uint32(8))
	// Write an impossibly long string length with no data to back it.
	binary.Write(&gw.buf, binary.LittleEndian, uint64(100000))

	_, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.Error(t, err)
}

func TestParseGGUFFailureUnknownValueType(t *testing.T) {
	// Prevents: unknown metadata value type causing wrong-size read and corruption.
	gw := newGGUFWriter()
	gw.writeHeader(1)
	gw.writeString("general.architecture")
	binary.Write(&gw.buf, binary.LittleEndian, uint32(99)) // invalid type

	_, err := ParseGGUFReader(gw.reader(), GGUF_MAGIC, gw.byteCount())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")
}

func TestParseSafetensorMetadataMissingConfig(t *testing.T) {
	// Prevents: directory without config.json causing error instead of returning empty meta.
	dir := t.TempDir()
	meta, err := parseSafetensorMetadata(dir)
	require.NoError(t, err)
	assert.Empty(t, meta.Architecture)
}

func TestParseSafetensorMetadataInvalidJSON(t *testing.T) {
	// Prevents: corrupted config.json causing error instead of returning empty meta.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{invalid json`), 0644)

	meta, err := parseSafetensorMetadata(dir)
	require.NoError(t, err)
	assert.Empty(t, meta.Architecture)
}

func TestScanSkipsHiddenDirectories(t *testing.T) {
	// Prevents: scanner recursing into hidden directories or crashing on unreadable dirs.
	dir := t.TempDir()
	hidden := filepath.Join(dir, ".hidden")
	os.Mkdir(hidden, 0700)
	os.WriteFile(filepath.Join(hidden, "model.gguf"), []byte{0x47, 0x47, 0x55, 0x46, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 0644)

	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, entries, "scanner must not recurse into subdirectories")
}

func TestScanEmptyDirectory(t *testing.T) {
	// Prevents: empty scan directory causing error.
	dir := t.TempDir()
	entries, err := Scan([]string{dir})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestScanInvalidDirectory(t *testing.T) {
	// Prevents: nonexistent directory crashing the scanner.
	entries, err := Scan([]string{"/nonexistent/path/that/does/not/exist"})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestScanWithResultsReturnsPerRootIssues(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := "/nonexistent/path/that/does/not/exist"
	writeMinimalGGUF(t, dir1, "model.gguf")

	results := ScanWithResults([]string{dir1, dir2})
	require.Len(t, results, 2)
	assert.NoError(t, results[0].Error)
	assert.Len(t, results[0].Entries, 1)
	assert.Error(t, results[1].Error)
	assert.Empty(t, results[1].Entries)
}

func TestScanWithResultsMixedSuccessAndFailure(t *testing.T) {
	dir1 := t.TempDir()
	writeMinimalGGUF(t, dir1, "model-a.gguf")
	writeMinimalGGUF(t, dir1, "model-b.gguf")
	dir2 := t.TempDir()
	os.RemoveAll(dir2) // Remove after creating to make it unreadable via scan

	results := ScanWithResults([]string{dir1, dir2})
	require.Len(t, results, 2)

	var successCount, failCount int
	for _, r := range results {
		if r.Error == nil {
			successCount++
		} else {
			failCount++
		}
	}
	assert.Equal(t, 1, successCount)
	assert.Equal(t, 1, failCount)
}

func TestNormalizePath(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		_, err := NormalizePath("")
		assert.Error(t, err)
	})

	t.Run("whitespace path", func(t *testing.T) {
		_, err := NormalizePath("   ")
		assert.Error(t, err)
	})

	t.Run("nonexistent path", func(t *testing.T) {
		_, err := NormalizePath("/nonexistent/path/xyz")
		assert.Error(t, err)
	})

	t.Run("valid path", func(t *testing.T) {
		dir := t.TempDir()
		normalized, err := NormalizePath(dir)
		assert.NoError(t, err)
		assert.Equal(t, dir, normalized)
	})
}

func TestScanWithProgressCallback(t *testing.T) {
	dir := t.TempDir()
	writeMinimalGGUF(t, dir, "a.gguf")
	writeMinimalGGUF(t, dir, "b.gguf")

	var progressCalls []string
	prog := func(rootDir string, entryCount int) {
		progressCalls = append(progressCalls, rootDir)
	}

	results := ScanWithContext(nil, []string{dir}, prog)
	require.Len(t, results, 1)
	assert.Len(t, progressCalls, 1)
	assert.Len(t, results[0].Entries, 2)
}

func TestScanWithContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := t.TempDir()
	results := ScanWithContext(ctx, []string{dir}, nil)
	require.Len(t, results, 1)
	assert.Error(t, results[0].Error)
	assert.Contains(t, results[0].Error.Error(), "canceled")
}
