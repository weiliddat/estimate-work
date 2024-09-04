package main

import (
	_ "embed"
	"html/template"
	"net/http"
	"strconv"
)

//go:embed templates/main.gohtml
var templateString string

var t = template.Must(template.New("index").Parse(templateString))

func index(w http.ResponseWriter, r *http.Request) {
	templateName := r.URL.Query().Get("template")
	if templateName == "" {
		templateName = "index"
	}

	count, err := strconv.Atoi(r.URL.Query().Get("count"))
	if err != nil {
		count = 0
	}

	counter := struct{ Next, Count int }{count + 1, count}

	t.ExecuteTemplate(w, templateName, counter)
}

func main() {
	http.HandleFunc("/", index)
	http.ListenAndServe("localhost:8080", nil)
}
