// Render the actual Docker inspection template against JSON fixtures using
// strict Go-template map lookups, matching the production client's behavior.
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
	parsed, err := template.New("inspect").Option("missingkey=error").Parse(input.Template)
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
