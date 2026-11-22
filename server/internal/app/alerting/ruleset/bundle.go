package ruleset

import (
	"bytes"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

// Document is one rule of a bundle (documents separated by "---" lines): its text, the line it
// starts on in the bundle, and the parsed rule or the error.
type Document struct {
	Source string
	Line   int
	Def    Definition
	Err    error
}

// ParseBundle reads the rules of a bundle (import, M2-17). Errors name the line in the whole
// text; documents with nothing but comments are skipped.
func ParseBundle(data []byte) []Document {
	var docs []Document
	text := string(data)
	start, line, startLine := 0, 1, 1
	flush := func(end int) {
		chunk := text[start:end]
		if !blank(chunk) {
			docs = append(docs, parseAt(chunk, startLine))
		}
	}
	for offset := 0; offset < len(text); {
		next := strings.IndexByte(text[offset:], '\n')
		lineEnd := len(text)
		if next >= 0 {
			lineEnd = offset + next + 1
		}
		if separator(strings.TrimRight(text[offset:lineEnd], "\r\n")) {
			flush(offset)
			start, startLine = lineEnd, line+1
		}
		offset = lineEnd
		line++
	}
	flush(len(text))
	return docs
}

func separator(line string) bool {
	if !strings.HasPrefix(line, "---") {
		return false
	}
	rest := strings.TrimSpace(line[3:])
	return rest == "" || strings.HasPrefix(rest, "#")
}

// blank reports whether a document holds only empty lines and comments.
func blank(chunk string) bool {
	for _, l := range strings.Split(chunk, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			return false
		}
	}
	return true
}

// parseAt parses a document of a bundle; empty lines in front keep the line numbers of the
// errors those of the bundle.
func parseAt(chunk string, line int) Document {
	d := Document{Source: chunk, Line: line}
	if len(chunk) > MaxDocumentBytes {
		d.Err = invalid("document too large (%d bytes, at most %d)", len(chunk), MaxDocumentBytes)
		return d
	}
	d.Def, d.Err = parse([]byte(strings.Repeat("\n", line-1) + chunk))
	if d.Err == nil {
		d.Def.Source = []byte(chunk)
	}
	return d
}

// YAML writes a rule document as YAML in block style (for rules without their own text).
func YAML(doc *webv1.AlertRule) ([]byte, error) {
	data, err := Marshal(doc)
	if err != nil {
		return nil, err
	}
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil, fmt.Errorf("render rule: %w", err)
	}
	block(&node)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return nil, fmt.Errorf("render rule: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render rule: %w", err)
	}
	return b.Bytes(), nil
}

func block(n *yaml.Node) {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style = 0
	}
	if n.Kind == yaml.ScalarNode && n.Style == yaml.DoubleQuotedStyle {
		n.Style = 0 // the encoder quotes where YAML needs it
	}
	for _, c := range n.Content {
		block(c)
	}
}

// Equal reports whether two rule documents say the same.
func Equal(a, b *webv1.AlertRule) bool { return proto.Equal(a, b) }
