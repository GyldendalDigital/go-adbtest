// Package ui parses Android uiautomator hierarchies and provides semantic
// native-UI interactions through adb.
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
	// Text is the element's visible text.
	Text string
	// ResourceID is the Android resource identifier from the hierarchy.
	ResourceID string
	// ContentDesc is the element's accessibility content description.
	ContentDesc string
	// Class is the fully qualified Android view class.
	Class string
	// Package is the package that owns the element.
	Package string
	// Clickable reports the hierarchy's clickable attribute.
	Clickable bool
	// Bounds is the element's screen rectangle in physical pixels.
	Bounds Rect
}

// Rect represents the bounding rectangle of a UI element.
type Rect struct {
	// X1 and Y1 are the top-left coordinates.
	X1, Y1 int
	// X2 and Y2 are the bottom-right coordinates.
	X2, Y2 int
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

var boundsRegex = regexp.MustCompile(`^\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]$`)

// ParseDump parses uiautomator XML dump into a flat slice of Elements.
func ParseDump(xmlData []byte) ([]Element, error) {
	var hierarchy xmlHierarchy
	if err := xml.Unmarshal(xmlData, &hierarchy); err != nil {
		return nil, fmt.Errorf("parse ui dump: %w", err)
	}

	var elements []Element
	for _, node := range hierarchy.Children {
		if err := flattenNode(node, &elements); err != nil {
			return nil, err
		}
	}
	return elements, nil
}

// flattenNode recursively flattens the XML tree into a slice.
func flattenNode(node xmlNode, elements *[]Element) error {
	bounds, err := parseBounds(node.Bounds)
	if err != nil {
		return fmt.Errorf("parse bounds for element text=%q resource-id=%q: %w", node.Text, node.ResourceID, err)
	}
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
		if err := flattenNode(child, elements); err != nil {
			return err
		}
	}
	return nil
}

// parseBounds parses a bounds string like "[0,0][1080,1920]" into a Rect.
func parseBounds(s string) (Rect, error) {
	matches := boundsRegex.FindStringSubmatch(s)
	if len(matches) != 5 {
		return Rect{}, fmt.Errorf("invalid bounds: %q", s)
	}
	values := make([]int, 4)
	for i, value := range matches[1:] {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Rect{}, fmt.Errorf("invalid bounds coordinate %q in %q: %w", value, s, err)
		}
		values[i] = parsed
	}
	x1, y1, x2, y2 := values[0], values[1], values[2], values[3]
	if x2 < x1 || y2 < y1 {
		return Rect{}, fmt.Errorf("invalid bounds ordering: %q", s)
	}
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
