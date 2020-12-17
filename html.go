// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"strings"

	"golang.org/x/net/html"
)

// findNodes returns nodes within the tree rooted at n for which f returns true.
// After a node is matched, its children are not examined.
func findNodes(n *html.Node, f func(*html.Node) bool) []*html.Node {
	var ns []*html.Node
	if f(n) {
		ns = append(ns, n)
	} else {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			ns = append(ns, findNodes(c, f)...)
		}
	}
	return ns
}

// findFirstNode performs a DFS on n, returning the first node for which f returns true.
func findFirstNode(n *html.Node, f func(*html.Node) bool) *html.Node {
	if f(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if m := findFirstNode(c, f); m != nil {
			return m
		}
	}
	return nil
}

// matchFunc is a convenient shorthand that returns a function that can be passed to findNodes.
// Only HTML elements with the given tag are matched. Expressions can take different forms:
//   - "name"      - "name" attribute must be present
//   - "name=val"  - "name" attribute must have value "val"
//   - "class=val" - "class" attribute must include "val"
func matchFunc(tag string, exprs ...string) func(n *html.Node) bool {
	return func(n *html.Node) bool {
		if tag != "" && !isElement(n, tag) {
			return false
		}

		for _, expr := range exprs {
			parts := strings.SplitN(expr, "=", 2)
			switch len(parts) {
			case 1:
				found := false
				for _, a := range n.Attr {
					if a.Key == parts[0] {
						found = true
						break
					}
				}
				if !found {
					return false
				}
			case 2:
				if parts[0] == "class" {
					if !hasClass(n, parts[1]) {
						return false
					}
				} else {
					if getAttr(n, parts[0]) != parts[1] {
						return false
					}
				}
			}
		}
		return true
	}
}

// isElement returns true if n is an HTML element with the supplied tag type.
func isElement(n *html.Node, tag string) bool {
	return n != nil && n.Type == html.ElementNode && n.Data == tag
}

// hasClass returns true if n's "class" attribute contains class.
func hasClass(n *html.Node, class string) bool {
	for _, v := range strings.Fields(getAttr(n, "class")) {
		if v == class {
			return true
		}
	}
	return false
}

// getAttr returns the first occurrence of the named attribute from n.
// An empty string is returned if the attribute isn't present.
func getAttr(n *html.Node, attr string) string {
	for _, a := range n.Attr {
		if a.Key == attr {
			return a.Val
		}
	}
	return ""
}

// deleteAttr recursively deletes attr from n and its descendents.
func deleteAttr(n *html.Node, attr string) {
	i := 0
	for _, a := range n.Attr {
		if a.Key != attr {
			n.Attr[i] = a
			i++
		}
	}
	n.Attr = n.Attr[:i]

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		deleteAttr(c, attr)
	}
}

// getText concatenates all text content in and under n.
func getText(n *html.Node) string {
	if n == nil {
		return ""
	}
	var text string
	if n.Type == html.TextNode {
		text += n.Data
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		text += getText(c)
	}
	return text
}
