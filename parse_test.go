// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"flag"
	"html/template"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/derat/htmlpretty"

	"github.com/google/go-cmp/cmp"

	"golang.org/x/net/html"
)

var updateGolden = flag.Bool("update-golden", false, "Update parse tests' golden files")

func TestParseTimeline(t *testing.T) {
	fns, err := filepath.Glob("testdata/*.html")
	if err != nil {
		t.Fatal("Failed globbing HTML files: ", err)
	}
	for _, fn := range fns {
		if strings.HasSuffix(fn, "-golden.html") {
			continue
		}

		df, err := os.Open(fn)
		if err != nil {
			t.Fatal("Failed opening HTML file: ", err)
		}
		defer df.Close()

		prof, tweets, err := parseTimeline(df, parseOptions{simplify: true})
		if err != nil {
			t.Errorf("Failed parsing %v: %v", fn, err)
			continue
		}

		// Write the parsed profile and tweets as a simple HTML document.
		var out bytes.Buffer
		tmpl := template.Must(template.New("").Funcs(map[string]interface{}{
			"Raw":  func(s string) template.HTML { return template.HTML(s) },
			"Time": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") },
		}).Parse(tweetsTmpl))
		if err := tmpl.Execute(&out, struct {
			Profile profile
			Tweets  []tweet
		}{prof, tweets}); err != nil {
			t.Fatal("Failed executing template: ", err)
		}

		// Pretty-print the HTML to make it easier to compare.
		root, err := html.Parse(&out)
		if err != nil {
			t.Fatalf("Output for %v is malformed: %v", fn, err)
		}
		out.Reset()
		if err := htmlpretty.Print(&out, root, "  ", 120); err != nil {
			t.Fatalf("Failed pretty-printing output for %v: %v", fn, err)
		}

		gfn := fn[:len(fn)-5] + "-golden.html"
		if *updateGolden {
			if err := ioutil.WriteFile(gfn, out.Bytes(), 0644); err != nil {
				t.Fatal("Failed writing golden file: ", err)
			}
		} else {
			golden, err := ioutil.ReadFile(gfn)
			if err != nil {
				t.Fatal("Failed reading golden file: ", err)
			}
			if diff := cmp.Diff(string(golden), out.String()); diff != "" {
				tf, err := ioutil.TempFile("", "twittuh.parse_test.*.html")
				if err != nil {
					t.Fatal("Failed creating temp file: ", err)
				}
				defer tf.Close()
				if _, err := tf.Write(out.Bytes()); err != nil {
					t.Fatalf("Failed writing %v: %v", tf.Name(), err)
				}
				t.Errorf("Didn't get expected output for %v:\n%v\n\nSee %v", fn, diff, tf.Name())
			}
		}
	}
}

func TestAddLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		orig, want string
	}{
		{"<div></div>", "<div></div>"},
		{"<div>\n</div>", "<div><br/>\n</div>"},
		{"<div>\n\n</div>", "<div><br/>\n<br/>\n</div>"},
		{"<div>word</div>", "<div>word</div>"},
		{"<div>word\n</div>", "<div>word<br/>\n</div>"},
		{"<div>two words</div>", "<div>two words</div>"},
		{"<div>first\nsecond</div>", "<div>first<br/>\nsecond</div>"},
		{"<div>double\n\nbreak</div>", "<div>double<br/>\n<br/>\nbreak</div>"},
		{"<div>\nleading\nand\ntrailing\n</div>", "<div><br/>\nleading<br/>\nand<br/>\ntrailing<br/>\n</div>"},
		{"<div> 1\n2</div> \n <div>3\n4 </div>", "<div> 1<br/>\n2</div> <br/>\n <div>3<br/>\n4 </div>"},
	} {
		root, err := html.Parse(strings.NewReader(tc.orig))
		if err != nil {
			t.Fatalf("Failed parsing %q: %v", tc.orig, err)
		}

		addLineBreaks(root)

		// Render back to a string.
		var b bytes.Buffer
		if err := html.Render(&b, root); err != nil {
			t.Fatal("Failed rendering tree: ", err)
		}

		// Drop uninteresting elements.
		got := b.String()
		got = strings.TrimPrefix(got, "<html><head></head><body>")
		got = strings.TrimSuffix(got, "</body></html>")

		if got != tc.want {
			t.Errorf("addLineBreaks(%q) = %q; want %q", tc.orig, got, tc.want)
		}
	}
}

const tweetsTmpl = `<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta http-equiv="Content-Security-Policy" content="script-src 'none'">
    <title>{{.Profile.Name}} (@{{.Profile.User}})</title>
    <style>
      body {
        font-family: Arial, Helvetica, sans-serif;
        max-width: 800px;
      }
      .profile {
        font-height: 24px;
        font-weight: bold;
        margin-left: 4px;
      }
      .profile img {
        height: 24px;
        width: 24px;
      }
      .profile .user { color: #888 }
      .tweet .head {
        font-weight: bold;
        margin: 8px;
      }
      .tweet .head .id { display: none }
      .tweet .head .user { color: #888 }
      .tweet .head .time { margin-left: 12px }
      .tweet .head a {
        color: black;
        text-decoration: none;
      }
      .tweet .content { margin: 4px }
      .tweet .content img, .tweet .content svg {
        max-height: 300px;
        max-width: 300px;
      }
      .tweet .text { display: none }
      .tweet hr { border: solid 1px #ddd }
      .sep { border: solid 1px black }
    </style>
  </head>
  <body>
    <div class="profile">
      <img src="{{.Profile.Icon}}">
      {{.Profile.Name}}
      <span class="user">@{{.Profile.User}}</span>
    </div>
    <hr class="sep">
    {{range .Tweets -}}
    <div class="tweet">
      <div class="head">
        <a href="{{.Href}}">
          <span class="id">{{.ID}}</span>
          {{.Name}}
          <span class="user">@{{.User}}</span>
          <span class="time">{{Time .Time}}</span>
        </a>
      </div>
      <hr>
      {{- if .ReplyUsers -}}
      <div class="reply">
        {{range .ReplyUsers}}{{.}}{{end}}
      </div>
      {{- end}}
      <div class="content">{{Raw .Content}}</div>
      <div class="text">{{.Text}}</div>
    </div>
    <hr class="sep">
    {{- end}}
  </body>
</html>`
