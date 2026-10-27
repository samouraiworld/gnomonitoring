package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/template"
)

func main() {
	var input struct {
		Template string         `json:"template"`
		Data     map[string]any `json:"data"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		panic(err)
	}
	parsed, err := template.New("inspect").Option("missingkey=error").Funcs(template.FuncMap{
		"json": func(value any) (string, error) {
			encoded, err := json.Marshal(value)
			return string(encoded), err
		},
		"split": strings.Split,
	}).Parse(input.Template)
	if err == nil {
		err = parsed.Execute(os.Stdout, input.Data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
