// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestFindNodes(t *testing.T) {
	for _, tc := range []struct {
		orig  string
		tag   string
		exprs []string
		want  string
	}{
		{`<p><img src="foo"/></p>`, "img", nil, `<img src="foo"/>`},
		{`<p><img src="foo"/></p>`, "img", []string{"src"}, `<img src="foo"/>`},
		{`<p><img src="foo"/></p>`, "img", []string{"src=foo"}, `<img src="foo"/>`},
		{`<p><img src="foo"/></p>`, "img", []string{"src=bar"}, ``}, // wrong attr value
		{`<p><img src="foo"/></p>`, "img", []string{"baz"}, ``},     // missing attr
		{`<p><img src="foo"/></p>`, "img", []string{"baz=bar"}, ``}, // missing attr
		{`<p><img src="foo"/></p>`, "strong", nil, ``},              // wrong tag
		{`<p><a class="a b"/>c</a></p>`, "a", []string{"class=a"}, `<a class="a b">c</a>`},
		{`<p><a class="a b"/>c</a></p>`, "a", []string{"class=b"}, `<a class="a b">c</a>`},
		{`<p><a class="a b"/>c</a></p>`, "a", []string{"class=d"}, ``}, // missing class value
		{`<p><p><strong>text</strong></p></p>`, "strong", nil, `<strong>text</strong>`},
		{`<div><p>abc</p>def<p>ghi</p></div>`, "p", nil, `<p>abc</p><p>ghi</p>`},
		{`<p a="b" c="d"></p><p a="b" e="f"></p>`, "p", []string{"a=b"}, `<p a="b" c="d"></p><p a="b" e="f"></p>`},
		{`<p a="b" c="d"></p><p a="b" e="f"></p>`, "p", []string{"a=b", "c=d"}, `<p a="b" c="d"></p>`},
	} {
		root, err := html.Parse(strings.NewReader(tc.orig))
		if err != nil {
			t.Fatalf("Failed parsing %q: %v", tc.orig, err)
		}

		div := &html.Node{Type: html.ElementNode, DataAtom: atom.Div, Data: "div"}
		for _, n := range findNodes(root, matchFunc(tc.tag, tc.exprs...)) {
			n.Parent.RemoveChild(n)
			div.AppendChild(n)
		}

		// Render matched nodes to a string and drop the wrapper we added.
		var b bytes.Buffer
		if err := html.Render(&b, div); err != nil {
			t.Fatal("Failed rendering tree: ", err)
		}
		got := b.String()
		got = strings.TrimPrefix(got, "<div>")
		got = strings.TrimSuffix(got, "</div>")

		if got != tc.want {
			t.Errorf("findNodes(%q) = %q; want %q", tc.orig, got, tc.want)
		}
	}
}

func TestGetText(t *testing.T) {
	const (
		markup    = "<html><body> a <div>b\nc<span> e <br/>f </span> g</div> h </body></html>"
		want      = " a b\nc e f  g h "
		wantSpace = "a b\nc e f g h"
	)
	root, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	if got := getText(root, false); got != want {
		t.Errorf("getText(%q, false) = %q; want %q", markup, got, want)
	}
	if got := getText(root, true); got != wantSpace {
		t.Errorf("getText(%q, true) = %q; want %q", markup, got, wantSpace)
	}
}
