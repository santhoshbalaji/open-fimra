// Command formcheck checks form definitions with the Go validator, so that
// programs in other languages, such as the Python geo worker, use the same
// rules and messages without a validator of their own.
//
// It reads one JSON value from the file named as its argument, or from
// standard input when there is none:
//
//   - a form definition gives {"problems": [...], "columns": [...]};
//   - an array of form definitions gives an array of those results, in order.
//
// "problems" is empty and "columns" lists the form's columns when the form is
// valid. "columns" is left out otherwise. The exit status is 0 whenever the
// input could be read, whether or not the forms are valid, 1 when it could
// not, and 2 for a usage mistake.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
)

// Result is what formcheck writes for one form.
type Result struct {
	Problems []string            `json:"problems"`
	Columns  []formschema.Column `json:"columns,omitempty"`
}

// maxInput keeps one call to a size that no real form batch reaches.
const maxInput = 64 << 20

func check(data []byte) Result {
	columns, problems := formschema.Columns(data)
	if problems == nil {
		problems = []string{}
	}
	return Result{Problems: problems, Columns: columns}
}

func run(input []byte, out io.Writer) error {
	trimmed := bytes.TrimSpace(input)
	var result any
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var forms []json.RawMessage
		if err := json.Unmarshal(trimmed, &forms); err != nil {
			return fmt.Errorf("the input is not a JSON array of forms: %w", err)
		}
		results := make([]Result, len(forms))
		for i, form := range forms {
			results[i] = check(form)
		}
		result = results
	} else {
		result = check(trimmed)
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	return enc.Encode(result)
}

func main() {
	var in io.Reader = os.Stdin
	switch len(os.Args) {
	case 1:
	case 2:
		file, err := os.Open(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, "formcheck:", err)
			os.Exit(1)
		}
		defer file.Close()
		in = file
	default:
		fmt.Fprintln(os.Stderr, "usage: formcheck [FILE]")
		os.Exit(2)
	}
	input, err := io.ReadAll(io.LimitReader(in, maxInput+1))
	if err == nil && len(input) > maxInput {
		err = fmt.Errorf("the input is larger than %d bytes", maxInput)
	}
	if err == nil {
		err = run(input, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "formcheck:", err)
		os.Exit(1)
	}
}
