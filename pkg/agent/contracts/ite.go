package contracts

// Tool is an executable action the model may call, described for the system
// prompt.
type Tool struct {
	Name        string
	Description string
	Parameters  ToolParameters
}


type ToolParameters struct {
	Type                 string         `json:"type"`
	Properties           map[string]any `json:"properties"`
	RequiredFields       []string       `json:"required"`
	AdditionalProperties bool           `json:"additionalProperties"`
}

type ToolType string

const (
	ReadTool ToolType = "read"
)
