package ui

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Element represents a UI element from the uiautomator hierarchy dump.
type Element struct {
	Text        string
	ResourceID  string
	ContentDesc string
	Class       string
	Package     string
	Clickable   bool
	Bounds      Rect
}

// Rect represents the bounding rectangle of a UI element.
type Rect struct {
	X1, Y1, X2, Y2 int
}

// CenterX returns the horizontal center of the rectangle.
func (r Rect) CenterX() int { return (r.X1 + r.X2) / 2 }

// CenterY returns the vertical center of the rectangle.
func (r Rect) CenterY() int { return (r.Y1 + r.Y2) / 2 }

// xmlNode mirrors the uiautomator XML node structure for parsing.
type xmlNode struct {
	XMLName     xml.Name  `xml:"node"`
	Text        string    `xml:"text,attr"`
	ResourceID  string    `xml:"resource-id,attr"`
	ContentDesc string    `xml:"content-desc,attr"`
	Class       string    `xml:"class,attr"`
	Package     string    `xml:"package,attr"`
	Clickable   string    `xml:"clickable,attr"`
	Bounds      string    `xml:"bounds,attr"`
	Children    []xmlNode `xml:"node"`
}

// xmlHierarchy is the root element of a uiautomator dump.
type xmlHierarchy struct {
	XMLName  xml.Name  `xml:"hierarchy"`
	Children []xmlNode `xml:"node"`
}

var boundsRegex = regexp.MustCompile(`\[(\d+),(\d+)\]\[(\d+),(\d+)\]`)

// ParseDump parses uiautomator XML dump into a flat slice of Elements.
func ParseDump(xmlData []byte) ([]Element, error) {
	var hierarchy xmlHierarchy
	if err := xml.Unmarshal(xmlData, &hierarchy); err != nil {
		return nil, fmt.Errorf("parse ui dump: %w", err)
	}

	var elements []Element
	for _, node := range hierarchy.Children {
		flattenNode(node, &elements)
	}
	return elements, nil
}

// flattenNode recursively flattens the XML tree into a slice.
func flattenNode(node xmlNode, elements *[]Element) {
	bounds, _ := parseBounds(node.Bounds)
	*elements = append(*elements, Element{
		Text:        node.Text,
		ResourceID:  node.ResourceID,
		ContentDesc: node.ContentDesc,
		Class:       node.Class,
		Package:     node.Package,
		Clickable:   node.Clickable == "true",
		Bounds:      bounds,
	})
	for _, child := range node.Children {
		flattenNode(child, elements)
	}
}

// parseBounds parses a bounds string like "[0,0][1080,1920]" into a Rect.
func parseBounds(s string) (Rect, error) {
	matches := boundsRegex.FindStringSubmatch(s)
	if len(matches) != 5 {
		return Rect{}, fmt.Errorf("invalid bounds: %q", s)
	}
	x1, _ := strconv.Atoi(matches[1])
	y1, _ := strconv.Atoi(matches[2])
	x2, _ := strconv.Atoi(matches[3])
	y2, _ := strconv.Atoi(matches[4])
	return Rect{X1: x1, Y1: y1, X2: x2, Y2: y2}, nil
}

// FindByText returns elements whose Text contains the substring (case-sensitive).
func FindByText(elements []Element, text string) []Element {
	var result []Element
	for _, e := range elements {
		if strings.Contains(e.Text, text) {
			result = append(result, e)
		}
	}
	return result
}

// FindByResourceID returns elements whose ResourceID matches exactly.
func FindByResourceID(elements []Element, id string) []Element {
	var result []Element
	for _, e := range elements {
		if e.ResourceID == id {
			result = append(result, e)
		}
	}
	return result
}

// FindByTextRegex returns elements whose Text matches the regex.
func FindByTextRegex(elements []Element, pattern string) ([]Element, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("compile regex %q: %w", pattern, err)
	}
	var result []Element
	for _, e := range elements {
		if re.MatchString(e.Text) {
			result = append(result, e)
		}
	}
	return result, nil
}
