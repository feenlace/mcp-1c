package dump

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// MetadataSelection selects configuration, object, or subsystem XML, never code modules.
type MetadataSelection struct {
	Kind       string `json:"kind"`
	ObjectType string `json:"object_type,omitempty"`
	Name       string `json:"name,omitempty"`
}
type MetadataSource struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type MetadataProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type MetadataAttribute struct {
	Name  string   `json:"name"`
	Types []string `json:"types"`
}
type MetadataTablePart struct {
	Name       string              `json:"name"`
	Attributes []MetadataAttribute `json:"attributes"`
}
type MetadataSummary struct {
	Selection    MetadataSelection   `json:"selection"`
	Name         string              `json:"name"`
	Version      string              `json:"version,omitempty"`
	Vendor       string              `json:"vendor,omitempty"`
	ObjectCounts map[string]int      `json:"object_counts,omitempty"`
	Properties   []MetadataProperty  `json:"properties,omitempty"`
	Attributes   []MetadataAttribute `json:"attributes,omitempty"`
	TableParts   []MetadataTablePart `json:"table_parts,omitempty"`
	Content      []string            `json:"content,omitempty"`
	Children     []string            `json:"children,omitempty"`
	Sources      []MetadataSource    `json:"sources"`
	Warnings     []string            `json:"warnings,omitempty"`
}

var errMetadataSource = errors.New("metadata XML source unavailable: select an existing declared source directory containing Configuration.xml")

const summaryMaxXML = 8 << 20

type summaryXML struct {
	name           string
	text           string
	children       []*summaryXML
	singletonNames map[string]bool
}

func (n *summaryXML) child(name string) *summaryXML {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}
func (n *summaryXML) value(name string) string {
	c := n.child(name)
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.text)
}
func summaryIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func summaryRead(ctx context.Context, root *os.Root, rel string) (*summaryXML, MetadataSource, error) {
	source := MetadataSource{Kind: "dump_xml", Path: filepath.ToSlash(rel)}
	// Refuse every symlink, including in-root links: source identities stay unambiguous.
	var selected os.FileInfo
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		st, err := root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return nil, source, errMetadataSource
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, source, errors.New("metadata source symlinks are unsupported")
		}
		if i == len(parts)-1 {
			selected = st
		}
		if i == len(parts)-1 && (!st.Mode().IsRegular() || st.Size() > summaryMaxXML) {
			return nil, source, errors.New("metadata XML must be a bounded regular file")
		}
	}
	f, err := root.OpenFile(rel, os.O_RDONLY|nonblockOpenFlag, 0)
	if err != nil {
		return nil, source, errMetadataSource
	}
	defer f.Close()
	current, statErr := f.Stat()
	if statErr != nil || !current.Mode().IsRegular() || !os.SameFile(selected, current) {
		return nil, source, errors.New("metadata source changed during read")
	}
	data, err := io.ReadAll(io.LimitReader(f, summaryMaxXML+1))
	if err != nil || len(data) > summaryMaxXML {
		return nil, source, errors.New("metadata XML read limit exceeded")
	}
	hash := sha256.Sum256(data)
	source.SHA256 = hex.EncodeToString(hash[:])
	dec := xml.NewDecoder(bytes.NewReader(data))
	base := &summaryXML{}
	stack := []*summaryXML{base}
	nodes := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, source, err
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, source, errors.New("malformed metadata XML")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			nodes++
			if len(stack) > 64 || nodes > 50000 {
				return nil, source, errors.New("metadata XML depth or element limit exceeded")
			}
			n := &summaryXML{name: t.Name.Local}
			parent := stack[len(stack)-1]
			if parent.name == "Properties" || n.name == "Properties" || n.name == "ChildObjects" {
				if parent.singletonNames == nil {
					parent.singletonNames = map[string]bool{}
				}
				if parent.singletonNames[n.name] {
					return nil, source, errors.New("duplicate metadata property or singleton container")
				}
				parent.singletonNames[n.name] = true
			}
			parent.children = append(parent.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].text += string(t)
		case xml.Directive:
			return nil, source, errors.New("metadata XML directives are unsupported")
		}
	}
	if len(base.children) != 1 {
		return nil, source, errors.New("metadata XML must contain one document element")
	}
	var checkChildren func(*summaryXML) error
	checkChildren = func(n *summaryXML) error {
		if n.name == "ChildObjects" {
			seen := map[string]bool{}
			for _, c := range n.children {
				identity := c.child("Properties").value("Name")
				if identity == "" {
					identity = strings.TrimSpace(c.text)
				}
				key := c.name + "." + identity
				if seen[key] {
					return errors.New("duplicate declared child identity")
				}
				seen[key] = true
			}
		}
		for _, c := range n.children {
			if err := checkChildren(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := checkChildren(base.children[0]); err != nil {
		return nil, source, err
	}
	return base.children[0], source, nil
}
func summaryObject(n *summaryXML, kind string) (*summaryXML, error) {
	if n.name == "MetaDataObject" {
		if len(n.children) != 1 {
			return nil, errors.New("ambiguous metadata XML identity")
		}
		n = n.children[0]
	}
	if n.name != kind {
		return nil, errors.New("unsupported metadata XML document kind")
	}
	props := 0
	for _, c := range n.children {
		if c.name == "Properties" {
			props++
		}
	}
	if props != 1 || n.child("Properties").value("Name") == "" {
		return nil, errors.New("metadata XML requires one declared identity")
	}
	seen := map[string]bool{}
	for _, p := range n.child("Properties").children {
		if seen[p.name] {
			return nil, errors.New("duplicate metadata property")
		}
		seen[p.name] = true
	}
	return n, nil
}
func summarySourcePrefix(root *os.Root) (string, error) {
	if _, err := root.Lstat("Configuration.xml"); err == nil {
		return "", nil
	}
	// Existing layout resolver owns extension recognition, including wrapped extensions.
	layout := detectExtensionLayoutMode(root.Name(), root)
	candidates := make([]string, 0, len(layout.byDir)+len(layout.byPrefix))
	for p := range layout.byDir {
		candidates = append(candidates, p)
	}
	for p := range layout.byPrefix {
		candidates = append(candidates, p)
	}
	// An ordinary configuration may sit immediately beneath a declared wrapper.
	entries, err := readDirInRoot(root, ".")
	if err != nil {
		return "", errMetadataSource
	}
	for _, entry := range entries {
		if entry.IsDir() {
			p := filepath.Join(entry.Name(), "Configuration.xml")
			if st, e := root.Lstat(p); e == nil && st.Mode().IsRegular() {
				candidates = append(candidates, entry.Name())
			}
		}
	}
	sort.Strings(candidates)
	candidates = uniqueSummary(candidates)
	if len(candidates) != 1 {
		return "", errors.New("metadata source root is missing or ambiguous; select one configuration or extension directory")
	}
	return candidates[0], nil
}
func uniqueSummary(v []string) []string {
	out := v[:0]
	for _, s := range v {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// ReadMetadataSummary reads only declared XML through a confined root; no index or live client is used.
func ReadMetadataSummary(ctx context.Context, dir string, sel MetadataSelection) (MetadataSummary, error) {
	result := MetadataSummary{Selection: sel, Sources: []MetadataSource{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if dir == "" {
		return result, errMetadataSource
	}
	st, err := os.Lstat(dir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return result, errMetadataSource
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return result, errMetadataSource
	}
	defer root.Close()
	prefix, err := summarySourcePrefix(root)
	if err != nil {
		return result, err
	}
	conf, source, err := summaryRead(ctx, root, filepath.Join(prefix, "Configuration.xml"))
	if err != nil {
		return result, err
	}
	conf, err = summaryObject(conf, "Configuration")
	if err != nil {
		return result, err
	}
	if sel.Kind == "" || sel.Kind == "configuration" {
		result.Selection.Kind = "configuration"
		result.Sources = append(result.Sources, source)
		props := conf.child("Properties")
		result.Name = props.value("Name")
		result.Version = props.value("Version")
		result.Vendor = props.value("Vendor")
		result.ObjectCounts = map[string]int{}
		seen := map[string]bool{}
		children := conf.child("ChildObjects")
		if children == nil {
			result.Warnings = append(result.Warnings, "configuration ChildObjects is missing")
		} else {
			for _, c := range children.children {
				key := c.name + "." + strings.TrimSpace(c.text)
				if seen[key] {
					return result, errors.New("duplicate declared metadata identity")
				}
				seen[key] = true
				result.ObjectCounts[c.name]++
			}
		}
		if result.Version == "" {
			result.Warnings = append(result.Warnings, "configuration Version is unavailable")
		}
		if result.Vendor == "" {
			result.Warnings = append(result.Warnings, "configuration Vendor is unavailable")
		}
		return result, nil
	}
	kind := sel.ObjectType
	if sel.Kind == "subsystem" {
		kind = "Subsystem"
	} else if sel.Kind != "object" {
		return result, errors.New("summary kind must be configuration, object or subsystem")
	}
	if kind == "Subsystem" {
		return readSubsystemSummary(ctx, root, dir, prefix, sel, result)
	}
	folder := objectTypeToDumpDir[kind]
	if kind == "Subsystem" {
		folder = "Subsystems"
	}
	if folder == "" || !summaryIdentifier(sel.Name) {
		return result, errors.New("select a supported metadata kind and identifier")
	}
	candidates := []string{filepath.Join(prefix, folder, sel.Name+".xml"), filepath.Join(prefix, folder, sel.Name, "Ext", sel.Name+".xml"), filepath.Join(prefix, kind, sel.Name+".xml"), filepath.Join(prefix, kind, sel.Name, "Ext", sel.Name+".xml")}
	var paths []string
	for _, p := range candidates {
		if _, e := root.Lstat(p); e == nil {
			paths = append(paths, p)
		}
	}
	if len(paths) != 1 {
		return result, errors.New("selected metadata XML is missing or ambiguous")
	}
	obj, source, err := summaryRead(ctx, root, paths[0])
	if err != nil {
		return result, err
	}
	obj, err = summaryObject(obj, kind)
	if err != nil {
		return result, err
	}
	result.Sources = append(result.Sources, source)
	props := obj.child("Properties")
	result.Name = props.value("Name")
	if result.Name != sel.Name {
		return result, errors.New("selected metadata XML identity does not match its name")
	}
	for _, p := range props.children {
		if p.name == "Name" {
			continue
		}
		if len(p.children) == 0 {
			result.Properties = append(result.Properties, MetadataProperty{p.name, strings.TrimSpace(p.text)})
			continue
		}
		if p.name == "Synonym" {
			for _, item := range p.children {
				if item.name == "item" {
					result.Properties = append(result.Properties, MetadataProperty{"Synonym." + item.value("lang"), item.value("content")})
				}
			}
			continue
		}
		result.Warnings = append(result.Warnings, "unsupported complex property: "+p.name)
	}
	if children := obj.child("ChildObjects"); children != nil {
		seen := map[string]bool{}
		for _, c := range children.children {
			name := c.child("Properties").value("Name")
			if name == "" {
				name = strings.TrimSpace(c.text)
			}
			key := c.name + "." + name
			if seen[key] {
				return result, errors.New("duplicate declared child identity")
			}
			seen[key] = true
			switch c.name {
			case "Attribute":
				result.Attributes = append(result.Attributes, summaryAttribute(c))
			case "TabularSection":
				part := MetadataTablePart{Name: name, Attributes: []MetadataAttribute{}}
				if cs := c.child("ChildObjects"); cs != nil {
					for _, a := range cs.children {
						if a.name == "Attribute" {
							part.Attributes = append(part.Attributes, summaryAttribute(a))
						}
					}
				}
				result.TableParts = append(result.TableParts, part)
			case "Subsystem":
				result.Children = append(result.Children, strings.TrimSpace(c.text))
			default:
				result.Warnings = append(result.Warnings, "unsupported child node: "+c.name)
			}
		}
	}
	return result, nil
}
func summaryAttribute(n *summaryXML) MetadataAttribute {
	p := n.child("Properties")
	a := MetadataAttribute{Name: p.value("Name"), Types: []string{}}
	if typ := p.child("Type"); typ != nil {
		for _, v := range typ.children {
			if v.name == "Type" {
				a.Types = append(a.Types, strings.TrimSpace(v.text))
			}
		}
	}
	return a
}

func readSubsystemSummary(ctx context.Context, root *os.Root, dir, prefix string, sel MetadataSelection, result MetadataSummary) (MetadataSummary, error) {
	subs, warnings, err := ParseAllSubsystemsCtx(ctx, filepath.Join(dir, prefix))
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, warnings...)
	var matches []Subsystem
	var walk func([]Subsystem)
	walk = func(nodes []Subsystem) {
		for _, s := range nodes {
			if s.Name == sel.Name || s.Path == sel.Name {
				matches = append(matches, s)
			}
			walk(s.Children)
		}
	}
	walk(subs)
	if len(matches) != 1 {
		return result, errors.New("selected subsystem is missing or ambiguous; use its full canonical path")
	}
	selected := matches[0]
	names := strings.Split(strings.TrimPrefix(selected.Path, "Подсистема."), ".")
	for _, n := range names {
		if !summaryIdentifier(n) {
			return result, errors.New("unsupported subsystem identity")
		}
	}
	hierarchical := filepath.Join(prefix, "Subsystems")
	for i, n := range names {
		if i == len(names)-1 {
			hierarchical = filepath.Join(hierarchical, n+".xml")
		} else {
			hierarchical = filepath.Join(hierarchical, n, "Subsystems")
		}
	}
	candidates := []string{hierarchical, filepath.Join(prefix, "Subsystems", names[0], "Ext", "Subsystem.xml")}
	var paths []string
	for _, p := range candidates {
		if _, e := root.Lstat(p); e == nil {
			paths = append(paths, p)
		}
	}
	if len(paths) != 1 {
		return result, errors.New("selected subsystem source is missing or ambiguous")
	}
	obj, source, err := summaryRead(ctx, root, paths[0])
	if err != nil {
		return result, err
	}
	obj, err = summaryObject(obj, "Subsystem")
	if err != nil {
		return result, err
	}
	expected := selected.Name
	if paths[0] != hierarchical {
		expected = names[0]
	}
	if obj.child("Properties").value("Name") != expected {
		return result, errors.New("subsystem source identity does not match selection")
	}
	result.Name = selected.Name
	result.Content = selected.Content
	result.Sources = append(result.Sources, source)
	for _, child := range selected.Children {
		result.Children = append(result.Children, child.Path)
	}
	return result, nil
}
