package report

import (
	"embed"
	"html/template"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// pageTemplate composes the templates/*.tmpl components. The entry point is the
// "report" template defined in report.tmpl.
var pageTemplate = template.Must(
	template.New("report").ParseFS(templatesFS, "templates/*.tmpl"),
)
