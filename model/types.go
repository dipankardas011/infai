package model

type ModelEntry struct {
	ID          int64
	ScanDir     string
	DirName     string
	GGUFPath    string
	MmprojPath  string
	DisplayName string
	Type        string
	Metadata    string
}

type GGUFMetadata struct {
	Architecture string
	ModelName    string
}

type Profile struct {
	ID              int64
	ModelID         int64
	Name            string
	Port            int
	Host            string
	ContextSize     int
	NGL             string
	BatchSize       *int
	UBatchSize      *int
	CacheTypeK      *string
	CacheTypeV      *string
	FlashAttn       bool
	Jinja           bool
	Temperature     *float64
	ReasoningBudget *int
	TopP            *float64
	TopK            *int
	NoKVOffload     bool
	UseMmproj       bool
	ExtraFlags      string
}
