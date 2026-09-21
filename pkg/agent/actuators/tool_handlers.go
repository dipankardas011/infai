package actuators

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/runenames"

	"github.com/dipankardas011/infai/pkg/agent/contracts"
)

// filesystemError is this package's internal failure shape. Tool executors
// convert it into a contracts.ExecutionError at their boundary.
type filesystemError struct {
	code           string
	reason         string
	responsibility contracts.FailureResponsibility
	cause          error
}

func (e *filesystemError) Error() string { return e.reason }

func (e *filesystemError) Unwrap() error { return e.cause }

func filesystemErr(code, reason string, responsibility contracts.FailureResponsibility, cause error) error {
	return &filesystemError{
		code:           code,
		reason:         reason,
		responsibility: responsibility,
		cause:          cause,
	}
}

func validateText(value string, responsibility contracts.FailureResponsibility) error {
	if !utf8.ValidString(value) {
		return filesystemErr("invalid_utf8", "the value is not valid UTF-8", responsibility, nil)
	}
	position := 0
	for _, character := range value {
		if character == '\r' || character == '\n' || character == '\t' {
			position++
			continue
		}
		if isInvisible(character) {
			description := fmt.Sprintf("U+%04X", character)
			if name := runenames.Name(character); name != "" {
				description += " " + name
			}
			return filesystemErr(
				"invisible_character",
				fmt.Sprintf("disallowed invisible character %s at character %d", description, position),
				responsibility,
				nil,
			)
		}
		position++
	}
	return nil
}

func isInvisible(character rune) bool {
	return unicode.IsControl(character) || unicode.In(
		character,
		unicode.Cf,
		unicode.Properties["Other_Default_Ignorable_Code_Point"],
		unicode.Properties["Variation_Selector"],
		unicode.Properties["Noncharacter_Code_Point"],
	)
}
