// Render the actual volume template against both Docker CLI representations.
package main

import (
	"encoding/json"
	"io"
	"os"
	"text/template"
)

type volume struct {
	Name, Driver, Scope string
	Options, Labels     map[string]string
}

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	source, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	value := volume{Name: "fixture", Driver: "local", Scope: "local", Labels: map[string]string{"com.docker.compose.project": "fixture"}}
	var data any = value
	if os.Args[1] == "map" {
		data = map[string]any{"Name": value.Name, "Driver": value.Driver, "Scope": value.Scope, "Options": value.Options, "Labels": value.Labels}
	} else if os.Args[1] != "struct" {
		os.Exit(2)
	}
	t, err := template.New("volume").Funcs(template.FuncMap{"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	}}).Option("missingkey=error").Parse(string(source))
	if err != nil || t.Execute(os.Stdout, data) != nil {
		os.Exit(1)
	}
}
