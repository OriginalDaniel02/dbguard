// Package liquibase reads Liquibase changelogs (XML, YAML, JSON) and translates
// their change types into the equivalent PostgreSQL statements, so the same risk
// rules used for Flyway SQL apply. It never executes anything.
package liquibase

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// node is a format-neutral element: an XML element, or a YAML/JSON mapping.
// Scalar attributes become attrs; nested structures become kids.
type node struct {
	name  string
	attrs map[string]string
	kids  []*node
	text  string
	line  int
}

func newNode(name string, line int) *node {
	return &node{name: name, attrs: map[string]string{}, line: line}
}

func (n *node) child(name string) *node {
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

func (n *node) children(name string) []*node {
	var out []*node
	for _, k := range n.kids {
		if k.name == name {
			out = append(out, k)
		}
	}
	return out
}

// attr returns an attribute, falling back to a child element's text (XML allows both forms).
func (n *node) attr(name string) string {
	if v, ok := n.attrs[name]; ok {
		return v
	}
	return ""
}

func parseXML(data []byte) (*node, error) {
	newlines := []int{}
	for i, b := range data {
		if b == '\n' {
			newlines = append(newlines, i)
		}
	}
	lineAt := func(off int64) int { return 1 + sort.SearchInts(newlines, int(off)) }

	d := xml.NewDecoder(bytes.NewReader(data))
	var root *node
	var stack []*node
	for {
		off := d.InputOffset()
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := newNode(t.Name.Local, lineAt(off))
			for _, a := range t.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil || root.name != "databaseChangeLog" {
		return nil, errors.New("not a Liquibase changelog (no <databaseChangeLog> root)")
	}
	return root, nil
}

func parseYAML(data []byte) (*node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML/JSON: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a Liquibase changelog (expected a databaseChangeLog key)")
	}
	top := doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == "databaseChangeLog" {
			return convert("databaseChangeLog", top.Content[i+1], top.Content[i].Line), nil
		}
	}
	return nil, errors.New("not a Liquibase changelog (no databaseChangeLog key)")
}

// convert maps a YAML node to the neutral tree. Wrapper keys that only hold a
// list (changes:, columns:) are dropped: the list items become children, which is
// exactly how the XML form looks.
func convert(name string, v *yaml.Node, line int) *node {
	n := newNode(name, line)
	switch v.Kind {
	case yaml.ScalarNode:
		n.text = v.Value
	case yaml.SequenceNode:
		addItems(n, v)
	case yaml.MappingNode:
		for i := 0; i+1 < len(v.Content); i += 2 {
			k, val := v.Content[i], v.Content[i+1]
			switch val.Kind {
			case yaml.ScalarNode:
				n.attrs[k.Value] = val.Value
			case yaml.MappingNode:
				n.kids = append(n.kids, convert(k.Value, val, k.Line))
			case yaml.SequenceNode:
				addItems(n, val)
			}
		}
	}
	return n
}

// addItems appends each single-key mapping item of a sequence as a child element.
func addItems(parent *node, seq *yaml.Node) {
	for _, item := range seq.Content {
		if item.Kind == yaml.MappingNode && len(item.Content) == 2 {
			k, val := item.Content[0], item.Content[1]
			parent.kids = append(parent.kids, convert(k.Value, val, k.Line))
		}
	}
}

func trimText(s string) string { return strings.TrimSpace(s) }
