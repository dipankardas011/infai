package contracts

// Tool is an executable action the model may call, described for the system
// prompt.
type Tool struct {
	Title       string
	Description string
}
