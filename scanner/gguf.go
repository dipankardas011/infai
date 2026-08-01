package scanner

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/dipankardas011/infai/model"
)

const (
	maxKVCount   = 2000    // safety: real GGUF files have 50–200 keys; 2000 means corrupt header
	maxKeyLen    = 1024    // safety: GGUF keys are at most ~50 chars; 1024 means corrupt key
	maxStringLen = 100 << 10 // safety: chat templates max ~10KB; 100KB is generous headroom against OOM
	maxArrayLen  = 10000   // safety: only for contiguous-alloc arrays (uint32/float32); string arrays bypass this
)

var ggufFTypes = map[int32]string{
	0:  "F32",
	1:  "F16",
	2:  "Q4_0",
	3:  "Q4_1",
	7:  "Q8_0",
	8:  "Q5_0",
	9:  "Q5_1",
	10: "Q2_K",
	11: "Q3_K_S",
	12: "Q3_K_M",
	13: "Q3_K_L",
	14: "Q4_K_S",
	15: "Q4_K_M",
	16: "Q5_K_S",
	17: "Q5_K_M",
	18: "Q6_K",
	19: "IQ2_XXS",
	20: "IQ2_XS",
	21: "Q2_K_S",
	22: "IQ3_XS",
	23: "IQ3_XXS",
	24: "IQ1_S",
	25: "IQ4_NL",
	26: "IQ3_S",
	27: "IQ3_M",
	28: "IQ2_S",
	29: "IQ2_M",
	30: "IQ4_XS",
	31: "IQ1_M",
	32: "BF16",
	33: "Q4_0_4_4",
	34: "Q4_0_4_8",
	35: "Q4_0_8_8",
	36: "TQ1_0",
	37: "TQ2_0",
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

	var magic uint32
	if err := binary.Read(f, binary.LittleEndian, &magic); err != nil {
		return nil, fmt.Errorf("read gguf magic: %w", err)
	}
	if magic != GGUF_MAGIC {
		return nil, fmt.Errorf("invalid gguf magic: 0x%08X", magic)
	}

	var version uint32
	if err := binary.Read(f, binary.LittleEndian, &version); err != nil {
		return nil, fmt.Errorf("read gguf version: %w", err)
	}
	if version < 1 || version > 3 {
		return nil, fmt.Errorf("unsupported gguf version %d", version)
	}

	var tensorCount uint64
	if err := binary.Read(f, binary.LittleEndian, &tensorCount); err != nil {
		return nil, fmt.Errorf("read gguf tensor count: %w", err)
	}
	_ = tensorCount

	var kvCount uint64
	if err := binary.Read(f, binary.LittleEndian, &kvCount); err != nil {
		return nil, fmt.Errorf("read gguf kv count: %w", err)
	}
	if kvCount > maxKVCount {
		return nil, fmt.Errorf("too many metadata keys: %d", kvCount)
	}

	kv, err := readGGUFMetadata(f, kvCount)
	if err != nil {
		return nil, err
	}

	meta := &model.ModelMetadata{FileSizeBytes: fi.Size()}
	hydrateGGUFMeta(meta, kv)
	return meta, nil
}

func readGGUFMetadata(r io.Reader, count uint64) (map[string]interface{}, error) {
	kv := make(map[string]interface{}, count)
	for i := uint64(0); i < count; i++ {
		key, val, err := readGGUFKeyValue(r)
		if err != nil {
			return nil, fmt.Errorf("kv[%d]: %w", i, err)
		}
		if len(key) > maxKeyLen {
			continue
		}
		kv[key] = val
	}
	return kv, nil
}

func readGGUFKeyValue(r io.Reader) (string, interface{}, error) {
	key, err := readGGUFString(r)
	if err != nil {
		return "", nil, fmt.Errorf("read key: %w", err)
	}

	var valType uint32
	if err := binary.Read(r, binary.LittleEndian, &valType); err != nil {
		return "", nil, fmt.Errorf("read value type: %w", err)
	}

	val, err := readGGUFValue(r, valType)
	if err != nil {
		return "", nil, err
	}
	return key, val, nil
}

func readGGUFValue(r io.Reader, valType uint32) (interface{}, error) {
	switch valType {
	case 0:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 1:
		var v int8
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 2:
		var v uint16
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 3:
		var v int16
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 4:
		var v uint32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 5:
		var v int32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 6:
		var v float32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 7:
		var v uint8
		if err := binary.Read(r, binary.LittleEndian, &v); err != nil {
			return nil, err
		}
		return v != 0, nil
	case 8:
		return readGGUFString(r)
	case 9:
		return readGGUFArray(r)
	case 10:
		var v uint64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 11:
		var v int64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 12:
		var v float64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	default:
		return nil, fmt.Errorf("unknown gguf value type: %d", valType)
	}
}

func readGGUFString(r io.Reader) (string, error) {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return "", err
	}
	if n > maxStringLen {
		return "", fmt.Errorf("string too long: %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func readGGUFArray(r io.Reader) (interface{}, error) {
	var elemType uint32
	if err := binary.Read(r, binary.LittleEndian, &elemType); err != nil {
		return nil, err
	}
	var length uint64
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return nil, err
	}

	switch elemType {
	case 4:
		if length > maxArrayLen {
			return nil, fmt.Errorf("array too long: %d", length)
		}
		arr := make([]uint32, length)
		for i := uint64(0); i < length; i++ {
			if err := binary.Read(r, binary.LittleEndian, &arr[i]); err != nil {
				return nil, err
			}
		}
		return arr, nil
	case 6:
		if length > maxArrayLen {
			return nil, fmt.Errorf("array too long: %d", length)
		}
		arr := make([]float32, length)
		for i := uint64(0); i < length; i++ {
			if err := binary.Read(r, binary.LittleEndian, &arr[i]); err != nil {
				return nil, err
			}
		}
		return arr, nil
	default:
		return skipGGUFArray(r, elemType, length)
	}
}

func skipGGUFArray(r io.Reader, elemType uint32, length uint64) (interface{}, error) {
	var elemSize uint64
	switch elemType {
	case 0, 1, 7:
		elemSize = 1
	case 2, 3:
		elemSize = 2
	case 4, 5, 6:
		elemSize = 4
	case 8:
		for i := uint64(0); i < length; i++ {
			if _, err := readGGUFString(r); err != nil {
				return nil, err
			}
		}
		return nil, nil
	case 10, 11, 12:
		elemSize = 8
	default:
		elemSize = 4
	}
	skip := elemSize * length
	discard := make([]byte, skip)
	if _, err := io.ReadFull(r, discard); err != nil {
		return nil, err
	}
	return nil, nil
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

	if v, ok := kv["tokenizer.ggml.model"]; ok {
		if s, ok := v.(string); ok {
			meta.TokenizerModel = s
		}
	}
	if v, ok := kv["tokenizer.chat_template"]; ok {
		if s, ok := v.(string); ok {
			meta.ChatTemplate = s
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
