package scanner

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dipankardas011/infai/model"
)

// read is a helper that reads a value of type T using the given byte order.
func read[T any](r io.Reader, order binary.ByteOrder) (T, error) {
	var v T
	err := binary.Read(r, order, &v)
	return v, err
}

// readCount reads a uint64 count value, using uint32 for GGUF v1 and uint64 for v2+.
func readCount(r io.Reader, order binary.ByteOrder, version int) (uint64, error) {
	if version == 1 {
		v, err := read[uint32](r, order)
		return uint64(v), err
	}
	return read[uint64](r, order)
}

const GGUF_MAGIC = 0x46554747

var ggufFTypes = map[int32]string{
	0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1",
	7: "Q8_0", 8: "Q5_0", 9: "Q5_1",
	10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L",
	14: "Q4_K_S", 15: "Q4_K_M", 16: "Q5_K_S", 17: "Q5_K_M", 18: "Q6_K",
	19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S", 22: "IQ3_XS", 23: "IQ3_XXS",
	24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M", 28: "IQ2_S", 29: "IQ2_M",
	30: "IQ4_XS", 31: "IQ1_M", 32: "BF16",
	33: "Q4_0_4_4", 34: "Q4_0_4_8", 35: "Q4_0_8_8",
	36: "TQ1_0", 37: "TQ2_0",
}

func ParseGGUF(path string) (*model.ModelMetadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gguf: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat gguf: %w", err)
	}

	return ParseGGUFReader(f, GGUF_MAGIC, fi.Size())
}

// ParseGGUFReader reads GGUF metadata from an io.ReadSeeker positioned at the
// start of the file. expectedMagic is typically GGUF_MAGIC; fileSize is used
// for the FileSizeBytes field.
func ParseGGUFReader(r io.ReadSeeker, expectedMagic uint32, fileSize int64) (*model.ModelMetadata, error) {
	// Read magic (always little-endian).
	var magic uint32
	if err := binary.Read(r, binary.LittleEndian, &magic); err != nil {
		return nil, fmt.Errorf("read gguf magic: %w", err)
	}
	if magic != expectedMagic {
		return nil, fmt.Errorf("invalid gguf magic: 0x%08X", magic)
	}

	// Detect endianness. After magic, seek to the last byte of the version
	// uint32 (byte 7). v3 big-endian files store version as [0,0,0,3],
	// little-endian as [3,0,0,0]. The last byte (position 7) is 0 for LE,
	// nonzero for BE.
	if _, err := r.Seek(3, io.SeekCurrent); err != nil {
		return nil, fmt.Errorf("seek to endian marker: %w", err)
	}
	var marker int8
	if err := binary.Read(r, binary.LittleEndian, &marker); err != nil {
		return nil, fmt.Errorf("read endian marker: %w", err)
	}
	order := binary.ByteOrder(binary.LittleEndian)
	if marker != 0 {
		order = binary.BigEndian
	}

	// Seek back to just after magic and read the version in native order.
	if _, err := r.Seek(4, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek to version: %w", err)
	}
	version, err := read[uint32](r, order)
	if err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if version < 1 || version > 3 {
		return nil, fmt.Errorf("unsupported gguf version %d", version)
	}

	tensorCount, err := readCount(r, order, int(version))
	if err != nil {
		return nil, fmt.Errorf("read tensor count: %w", err)
	}
	_ = tensorCount

	kvCount, err := readCount(r, order, int(version))
	if err != nil {
		return nil, fmt.Errorf("read kv count: %w", err)
	}

	kv, err := readGGUFMetadata(r, order, int(version), kvCount)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	meta := &model.ModelMetadata{FileSizeBytes: fileSize}
	hydrateGGUFMeta(meta, kv)
	if meta.NumExperts > 0 && tensorCount > 0 {
		alignment := uint64(32)
		if value, ok := getUint32(kv, "general.alignment"); ok && value > 0 {
			alignment = uint64(value)
		}
		meta.MoEExpertBytes = readGGUFExpertBytes(r, order, int(version), tensorCount, fileSize, alignment)
	}
	return meta, nil
}

type ggufTensorOffset struct {
	name   string
	offset uint64
}

func readGGUFExpertBytes(r io.ReadSeeker, order binary.ByteOrder, version int, count uint64, fileSize int64, alignment uint64) uint64 {
	tensors := make([]ggufTensorOffset, 0, count)
	for i := uint64(0); i < count; i++ {
		name, err := readGGUFString(r, order, version)
		if err != nil {
			return 0
		}
		dims, err := read[uint32](r, order)
		if err != nil {
			return 0
		}
		for j := uint32(0); j < dims; j++ {
			if _, err := read[uint64](r, order); err != nil {
				return 0
			}
		}
		if _, err := read[uint32](r, order); err != nil {
			return 0
		}
		offset, err := read[uint64](r, order)
		if err != nil {
			return 0
		}
		tensors = append(tensors, ggufTensorOffset{name: name, offset: offset})
	}
	current, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0
	}
	if alignment == 0 {
		alignment = 32
	}
	dataStart := (uint64(current) + alignment - 1) &^ (alignment - 1)
	if dataStart >= uint64(fileSize) {
		return 0
	}
	dataEnd := uint64(fileSize) - dataStart
	sort.Slice(tensors, func(i, j int) bool { return tensors[i].offset < tensors[j].offset })
	var expertBytes uint64
	for i, tensor := range tensors {
		end := dataEnd
		if i+1 < len(tensors) {
			end = tensors[i+1].offset
		}
		if end <= tensor.offset || !isGGUFExpertTensor(tensor.name) {
			continue
		}
		expertBytes += end - tensor.offset
	}
	return expertBytes
}

func isGGUFExpertTensor(name string) bool {
	return strings.Contains(name, "_exps") || strings.Contains(name, ".experts.")
}

// readGGUFString reads a length-prefixed GGUF string.
func readGGUFString(r io.Reader, order binary.ByteOrder, version int) (string, error) {
	n, err := readCount(r, order, version)
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

// readGGUFValue reads a single GGUF metadata value (scalar or array).
func readGGUFValue(r io.Reader, order binary.ByteOrder, version int, valType uint32) (interface{}, error) {
	switch valType {
	case 0:
		return read[uint8](r, order)
	case 1:
		return read[int8](r, order)
	case 2:
		return read[uint16](r, order)
	case 3:
		return read[int16](r, order)
	case 4:
		return read[uint32](r, order)
	case 5:
		return read[int32](r, order)
	case 6:
		return read[float32](r, order)
	case 7:
		v, err := read[uint8](r, order)
		if err != nil {
			return nil, err
		}
		return v != 0, nil
	case 8:
		return readGGUFString(r, order, version)
	case 9:
		return readGGUFArray(r, order, version)
	case 10:
		return read[uint64](r, order)
	case 11:
		return read[int64](r, order)
	case 12:
		return read[float64](r, order)
	default:
		return nil, fmt.Errorf("unknown gguf value type: %d", valType)
	}
}

// readGGUFArray reads a GGUF array value.
func readGGUFArray(r io.Reader, order binary.ByteOrder, version int) (interface{}, error) {
	elemType, err := read[uint32](r, order)
	if err != nil {
		return nil, err
	}
	length, err := readCount(r, order, version)
	if err != nil {
		return nil, err
	}

	switch elemType {
	case 4:
		return readArray[uint32](r, order, length)
	case 6:
		return readArray[float32](r, order, length)
	case 7:
		return readArray[bool](r, order, length)
	case 8:
		arr := make([]string, length)
		for i := uint64(0); i < length; i++ {
			s, err := readGGUFString(r, order, version)
			if err != nil {
				return nil, err
			}
			arr[i] = s
		}
		return arr, nil
	default:
		return discardGGUFArray(r, elemType, length)
	}
}

func readArray[T any](r io.Reader, order binary.ByteOrder, length uint64) ([]T, error) {
	arr := make([]T, length)
	for i := uint64(0); i < length; i++ {
		v, err := read[T](r, order)
		if err != nil {
			return nil, err
		}
		arr[i] = v
	}
	return arr, nil
}

func discardGGUFArray(r io.Reader, elemType uint32, length uint64) (interface{}, error) {
	var elemSize uint64
	switch elemType {
	case 0, 1, 7:
		elemSize = 1
	case 2, 3:
		elemSize = 2
	case 4, 5, 6:
		elemSize = 4
	case 10, 11, 12:
		elemSize = 8
	default:
		elemSize = 1
	}
	if _, err := io.CopyN(io.Discard, r, int64(elemSize*length)); err != nil {
		return nil, err
	}
	return nil, nil
}

// interestingPrefix reports whether the key is needed for metadata extraction.
func interestingPrefix(key string) bool {
	for _, p := range []string{
		"general.",
		"tokenizer.ggml.model",
	} {
		if len(key) >= len(p) && key[:len(p)] == p {
			return true
		}
	}
	// Architecture-specific keys like llama.context_length, llama.embedding_length, etc.
	// These all have the form {arch}.xxx — check for known suffixes.
	for _, s := range []string{
		".context_length", ".embedding_length", ".block_count",
		".feed_forward_length", ".attention.head_count", ".attention.head_count_kv",
		".attention.key_length", ".attention.sliding_window", ".attention.sliding_window_pattern",
		".attention.shared_kv_layers", ".nextn_predict_layers", ".vocab_size",
		".expert_count", ".expert_used_count",
	} {
		if len(key) > len(s) && key[len(key)-len(s):] == s {
			return true
		}
	}
	return false
}

func readGGUFMetadata(r io.Reader, order binary.ByteOrder, version int, count uint64) (map[string]interface{}, error) {
	kv := make(map[string]interface{})
	for i := uint64(0); i < count; i++ {
		key, err := readGGUFString(r, order, version)
		if err != nil {
			return nil, fmt.Errorf("kv[%d] key: %w", i, err)
		}

		valType, err := read[uint32](r, order)
		if err != nil {
			return nil, fmt.Errorf("kv[%d] value type: %w", i, err)
		}

		if interestingPrefix(key) {
			val, err := readGGUFValue(r, order, version, valType)
			if err != nil {
				return nil, fmt.Errorf("kv[%d] %q: %w", i, key, err)
			}
			kv[key] = val
		} else {
			if _, err := skipGGUFValue(r, order, version, valType); err != nil {
				return nil, fmt.Errorf("kv[%d] skip %q: %w", i, key, err)
			}
		}
	}
	return kv, nil
}

func skipGGUFValue(r io.Reader, order binary.ByteOrder, version int, valType uint32) (interface{}, error) {
	switch valType {
	case 0, 1, 7:
		var buf [1]byte
		_, err := io.ReadFull(r, buf[:])
		return nil, err
	case 2, 3:
		var buf [2]byte
		_, err := io.ReadFull(r, buf[:])
		return nil, err
	case 4, 5, 6:
		var buf [4]byte
		_, err := io.ReadFull(r, buf[:])
		return nil, err
	case 8:
		return readGGUFString(r, order, version)
	case 9:
		elemType, err := read[uint32](r, order)
		if err != nil {
			return nil, err
		}
		length, err := readCount(r, order, version)
		if err != nil {
			return nil, err
		}
		return skipGGUFArray(r, order, version, elemType, length)
	case 10, 11, 12:
		var buf [8]byte
		_, err := io.ReadFull(r, buf[:])
		return nil, err
	default:
		return nil, fmt.Errorf("unknown gguf value type: %d", valType)
	}
}

func skipGGUFArray(r io.Reader, order binary.ByteOrder, version int, elemType uint32, length uint64) (interface{}, error) {
	if length == 0 {
		return nil, nil
	}
	switch elemType {
	case 8:
		for i := uint64(0); i < length; i++ {
			if _, err := readGGUFString(r, order, version); err != nil {
				return nil, err
			}
		}
		return nil, nil
	default:
		return discardGGUFArray(r, elemType, length)
	}
}

func hydrateGGUFMeta(meta *model.ModelMetadata, kv map[string]interface{}) {
	if v, ok := kv["general.architecture"]; ok {
		if s, ok := v.(string); ok {
			meta.Architecture = s
		}
	}
	if v, ok := kv["general.name"]; ok {
		if s, ok := v.(string); ok {
			meta.ModelName = s
		}
	}
	if v, ok := kv["general.file_type"]; ok {
		switch ft := v.(type) {
		case uint32:
			meta.FileType = int32(ft)
		case int32:
			meta.FileType = ft
		case uint64:
			meta.FileType = int32(ft)
		}
	}
	if meta.FileType != 0 {
		if name, ok := ggufFTypes[meta.FileType]; ok {
			meta.Quantization = name
		}
	}
	if v, ok := kv["general.parameter_count"]; ok {
		switch pc := v.(type) {
		case uint64:
			meta.ParameterCount = pc
		case uint32:
			meta.ParameterCount = uint64(pc)
		}
	}

	arch := "general"
	if meta.Architecture != "" {
		arch = meta.Architecture
	}

	if v, ok := getUint32(kv, arch+".context_length"); ok {
		meta.ContextLength = v
	}
	if v, ok := getUint32(kv, arch+".embedding_length"); ok {
		meta.EmbeddingLength = v
	}
	if v, ok := getUint32(kv, arch+".block_count"); ok {
		meta.BlockCount = v
	}
	if v, ok := getUint32(kv, arch+".feed_forward_length"); ok {
		meta.FeedForwardLength = v
	}
	if v, ok := getUint32(kv, arch+".attention.head_count"); ok {
		meta.AttentionHeadCount = v
	}
	if v, ok := getUint32(kv, arch+".attention.head_count_kv"); ok {
		meta.AttentionHeadCountKV = v
	}
	if v, ok := getUint32(kv, arch+".vocab_size"); ok {
		meta.VocabSize = v
	}

	if v, ok := getUint32(kv, arch+".attention.key_length"); ok {
		meta.HeadDimension = v
	} else if meta.EmbeddingLength > 0 && meta.AttentionHeadCount > 0 {
		meta.HeadDimension = meta.EmbeddingLength / meta.AttentionHeadCount
	}

	if v, ok := getUint32(kv, arch+".expert_count"); ok {
		meta.NumExperts = v
	}
	if v, ok := getUint32(kv, arch+".expert_used_count"); ok {
		meta.NumExpertsPerToken = v
	}
	if v, ok := getUint32(kv, arch+".attention.sliding_window"); ok {
		meta.SlidingWindow = v
	}
	if v, ok := getUint32(kv, arch+".attention.shared_kv_layers"); ok {
		meta.KVCacheSharedLayers = v
	}
	if v, ok := getUint32(kv, arch+".nextn_predict_layers"); ok {
		meta.MTPNumLayers = v
	}
	if pattern, ok := kv[arch+".attention.sliding_window_pattern"].([]bool); ok {
		meta.AttentionLayerTypes = make([]string, len(pattern))
		for i, isSliding := range pattern {
			if isSliding {
				meta.AttentionLayerTypes[i] = "sliding_attention"
			} else {
				meta.AttentionLayerTypes[i] = "full_attention"
				meta.GlobalAttentionLayers++
			}
		}
	}

	if v, ok := kv["tokenizer.ggml.model"]; ok {
		if s, ok := v.(string); ok {
			meta.TokenizerModel = s
		}
	}
}

func getUint32(kv map[string]interface{}, key string) (uint32, bool) {
	if v, ok := kv[key]; ok {
		switch vv := v.(type) {
		case uint32:
			return vv, true
		case int32:
			if vv >= 0 {
				return uint32(vv), true
			}
		case uint64:
			if vv <= uint64(^uint32(0)) {
				return uint32(vv), true
			}
		case int64:
			if vv >= 0 && vv <= int64(^uint32(0)) {
				return uint32(vv), true
			}
		case float32:
			if vv >= 0 {
				return uint32(vv), true
			}
		}
	}
	return 0, false
}
