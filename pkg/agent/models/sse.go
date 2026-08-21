package models

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// Event is one parsed Server-Sent Event.
type Event struct {
	Data  string
	Event string
	ID    string
	Retry int
}

// Decoder parses Server-Sent Events per the W3C EventSource spec: line-based
// fields, "data" lines accumulate joined with "\n", events are dispatched on
// blank lines, ":" lines are comments, and EOF dispatches a pending event.
// OpenAI-compatible model streams (OpenAI, DeepSeek, vLLM, llama.cpp, …) are
// valid SSE, so this handles every modern provider.
type Decoder struct {
	r *bufio.Reader
}

func NewDecoder(r io.Reader) *Decoder { return &Decoder{r: bufio.NewReader(r)} }

func (d *Decoder) Decode() (Event, error) {
	var ev Event
	var data []string

	for {
		line, err := d.readLine()
		if err != nil {
			if err == io.EOF && (len(data) > 0 || ev.Event != "" || ev.ID != "") {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			return ev, err
		}

		if line == "" { // blank line -> dispatch the accumulated event
			if len(data) > 0 || ev.Event != "" || ev.ID != "" {
				ev.Data = strings.Join(data, "\n")
				return ev, nil
			}
			continue
		}
		if line[0] == ':' { // comment line, ignored
			continue
		}

		field, value := parseField(line)
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			ev.Event = value
		case "id":
			ev.ID = value
		case "retry":
			ev.Retry, _ = strconv.Atoi(value)
		}
	}
}

// readLine reads a single line, accepting \r\n, \n, or \r endings.
func (d *Decoder) readLine() (string, error) {
	line, err := d.r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// parseField splits "field:value". A missing colon means an empty value; a
// single leading space after the colon is stripped per the spec.
func parseField(line string) (field, value string) {
	field, value, _ = strings.Cut(line, ":")
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return field, value
}
