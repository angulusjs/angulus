// Package compiler parses Angulus templates and emits reactive DOM factories.
package compiler

type Diagnostic struct {
	File     string `json:"file"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type Attribute struct {
	Name       string `json:"name"`
	Value      string `json:"value"`
	Start      int    `json:"start"`
	ValueStart int    `json:"valueStart"`
}

type Case struct {
	Expression string `json:"expression"`
	ExprStart  int    `json:"exprStart"`
	Children   []Node `json:"children"`
}

type Node struct {
	Kind       string      `json:"kind"`
	Start      int         `json:"start"`
	End        int         `json:"end"`
	Text       string      `json:"text,omitempty"`
	Tag        string      `json:"tag,omitempty"`
	Attributes []Attribute `json:"attributes,omitempty"`
	Children   []Node      `json:"children,omitempty"`
	Otherwise  []Node      `json:"otherwise,omitempty"`
	Expression string      `json:"expression,omitempty"`
	ExprStart  int         `json:"exprStart,omitempty"`
	Item       string      `json:"item,omitempty"`
	Track      string      `json:"track,omitempty"`
	TrackStart int         `json:"trackStart,omitempty"`
	Cases      []Case      `json:"cases,omitempty"`
}

type ParseResult struct {
	Nodes       []Node       `json:"nodes"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type GenerateResult struct {
	Code        string       `json:"code"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Mappings    []Mapping    `json:"mappings,omitempty"`
}

// Mapping anchors a UTF-16 offset in generated factory code to the template.
type Mapping struct {
	Generated int `json:"generated"`
	Source    int `json:"source"`
}
