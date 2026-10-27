// Render captured Docker inspection templates with production-style strict map
// lookups. The json function matches Docker's inspection template helper.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/template"
)

func render() error {
	var input struct {
		Template string         `json:"template"`
		Data     map[string]any `json:"data"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		return err
	}
	parsed, err := template.New("inspect").Option("missingkey=error").Funcs(template.FuncMap{
		"json": func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			return string(encoded), err
		},
	}).Parse(input.Template)
	if err != nil {
		return err
	}
	return parsed.Execute(os.Stdout, input.Data)
}

func main() {
	if err := render(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
