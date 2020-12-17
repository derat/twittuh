// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/gorilla/feeds"
)

// feedFormat describes different feed formats that can be written.
type feedFormat string

const (
	atomFormat feedFormat = "atom"
	jsonFormat feedFormat = "json"
	rssFormat  feedFormat = "rss"
)

const (
	titleLen = 80 // max length of title text in feed, in runes

	defaultMode os.FileMode = 0644 // default mode for new feed files
)

var verbose = false // enable verbose logging

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <user> <file>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	browserSize := flag.String("browser-size", "1024x8192", "Browser viewport size")
	debugChrome := flag.Bool("debug-chrome", false, "Log noisy Chrome debug messages")
	debugFile := flag.String("debug-file", "", "HTML timeline file to parse for debugging")
	dumpDOM := flag.Bool("dump-dom", false, "Dump the timeline DOM to stdout for debugging")
	force := flag.Bool("force", false, "Write feed even if there are no new tweets")
	formatFlag := flag.String("format", "atom", `Feed format to write ("atom", "json", "rss")`)
	replies := flag.Bool("replies", false, "Include the user's replies")
	skipUsers := flag.String("skip-users", "", "Comma-separated users whose tweets should be skipped")
	timeout := flag.Int("timeout", 0, "Chrome timeout in seconds")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")
	flag.Parse()

	ctx := context.Background()
	cancel := func() {}
	if *timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*timeout)*time.Second)
	}
	defer cancel()

	if *debugFile != "" {
		if err := debugParse(*debugFile, *replies); err != nil {
			log.Fatal("Failed reading timeline: ", err)
		}
		os.Exit(0)
	}

	if len(flag.Args()) != 2 {
		flag.Usage()
		os.Exit(2)
	}
	user := bareUser(flag.Arg(0))
	feedPath := flag.Arg(1)
	format := feedFormat(*formatFlag)

	ps := strings.Split(*browserSize, "x")
	if len(ps) != 2 {
		log.Fatalf("Bad browser size %q", *browserSize)
	}
	width, werr := strconv.Atoi(ps[0])
	height, herr := strconv.Atoi(ps[1])
	if werr != nil || herr != nil {
		log.Fatalf("Bad browser size %q", *browserSize)
	}

	var oldLatestID int64
	var err error
	if !*force {
		if oldLatestID, err = getLatestID(feedPath, format); err != nil {
			log.Printf("Couldn't get old latest ID from %v: %v", feedPath, err)
		}
	}

	debugf("Getting timeline for %v with old latest ID %v", user, oldLatestID)
	dom, err := fetchTimeline(ctx, user, width, height, *debugChrome)
	if err != nil {
		log.Fatalf("Failed fetching timeline for %v: %v", user, err)
	}
	if *dumpDOM {
		os.Stdout.WriteString(dom)
	}
	prof, tweets, err := parseTimeline(strings.NewReader(dom))
	if err != nil {
		log.Fatalf("Failed parsing timeline for %v: %v", user, err)
	} else if len(tweets) == 0 {
		log.Fatalf("No tweets found for %v", user)
	}
	debugf("Parsed %v tweet(s)", len(tweets))

	var latestID int64
	for _, tw := range tweets {
		if tw.id > latestID {
			latestID = tw.id
		}
	}
	if latestID == oldLatestID {
		debug("No new tweets; exiting without writing feed")
		os.Exit(0)
	}

	// Write to a temp file and then replace the feed atomically to preserve the old version if
	// something goes wrong.
	f, err := ioutil.TempFile(filepath.Dir(feedPath), "."+filepath.Base(feedPath)+".")
	if err != nil {
		log.Fatal("Failed creating feed file: ", err)
	}
	defer os.Remove(f.Name()) // fails if we successfully rename temp file

	if err := writeFeed(f, format, prof, tweets, latestID, *replies, strings.Split(*skipUsers, ",")); err != nil {
		f.Close()
		log.Fatal("Failed writing feed: ", err)
	}
	if err := f.Close(); err != nil {
		log.Fatal("Failed closing feed file: ", err)
	}
	mode := defaultMode // ioutil.TempFile seems to use 0600 by default
	if fi, err := os.Stat(feedPath); err == nil {
		mode = fi.Mode()
	}
	if err := os.Chmod(f.Name(), mode); err != nil {
		log.Print("Failed setting mode: ", err)
	}
	if err := os.Rename(f.Name(), feedPath); err != nil {
		log.Fatal("Failed replacing feed file: ", err)
	}
}

// fetchTimeline fetches the timeline page for the supplied user and returns its full DOM.
func fetchTimeline(ctx context.Context, user string, width, height int, debug bool) (string, error) {
	copts := []chromedp.ContextOption{
		chromedp.WithLogf(log.Printf),
		chromedp.WithErrorf(log.Printf),
	}
	if debug {
		copts = append(copts, chromedp.WithDebugf(log.Printf))
	}
	ctx, cancel := chromedp.NewContext(ctx, copts...)
	defer cancel()

	// TODO: Is it necessary to wait longer?
	var data string
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(userURL(user)),
		chromedp.WaitVisible(`div[data-testid="tweet"]`),
		chromedp.Evaluate(`document.documentElement.outerHTML`, &data),
	)
	return data, err
}

// writeFeed writes a feed in the supplied format containing tweets from a user's timeline.
// If replies is true, the user's replies will also be included.
func writeFeed(w io.Writer, format feedFormat, prof profile, tweets []tweet,
	latestID int64, replies bool, skipUsers []string) error {
	author := prof.displayName()
	feedDesc := "Tweets"
	if replies {
		feedDesc += " and replies"
	}
	feedDesc += fmt.Sprintf(" from @%v's timeline", prof.user)

	feed := &feeds.Feed{
		Title:       author,
		Link:        &feeds.Link{Href: userURL(prof.user)},
		Description: feedDesc,
		Author:      &feeds.Author{Name: author},
		Updated:     time.Now(),
		Copyright:   fmt.Sprintf("© %v %v", time.Now().Year(), author),
	}
	if prof.image != "" {
		feed.Image = &feeds.Image{Url: prof.image}
	}

	// User-supplied names may not have the canonical casing.
	skipUsersMap := make(map[string]struct{})
	for _, u := range skipUsers {
		skipUsersMap[strings.ToLower(bareUser(u))] = struct{}{}
	}

	for _, t := range tweets {
		if !replies && t.reply() {
			continue
		}
		if _, ok := skipUsersMap[strings.ToLower(t.user)]; ok && t.user != prof.user {
			continue
		}

		item := &feeds.Item{
			Title:       t.text,
			Link:        &feeds.Link{Href: t.href}, // Atom's default rel is "alternate"
			Description: t.text,
			Author:      &feeds.Author{Name: t.displayName()},
			Id:          fmt.Sprintf("%v", t.id),
			Created:     t.time,
			Updated:     t.time,
			Content:     t.content,
		}
		if ut := []rune(item.Title); len(ut) > titleLen {
			item.Title = string(ut[:titleLen-1]) + "…"
		}
		feed.Add(item)
	}

	debugf("Writing feed with %v item(s) and latest ID %v", len(feed.Items), latestID)

	switch format {
	case jsonFormat:
		// Embed the latest ID in the feed's UserComment field.
		// The marshaling here matches feeds.Feed.WriteJSON().
		jf := (&feeds.JSON{Feed: feed}).JSONFeed()
		jf.UserComment = fmt.Sprintf("latest id %v", latestID)
		jf.Favicon = prof.icon
		jf.Icon = prof.image
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(jf)
	case atomFormat, rssFormat:
		var err error
		if format == atomFormat {
			err = feed.WriteAtom(w)
		} else {
			err = feed.WriteRss(w)
		}
		if err != nil {
			return err
		}
		// Embed the latest ID in a trailing comment.
		_, err = fmt.Fprintf(w, "\n<!-- latest id %v -->\n", latestID)
		return err
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// These match the comments added by writeFeed.
var xmlLatestIDRegexp = regexp.MustCompile(`<!--\s+latest\s+id\s+(\d+)\s+-->\s*$`)
var jsonLatestIDRegexp = regexp.MustCompile(`^latest id (\d+)$`)

// getLatestID attempts to find the latest tweet ID embedded in p, a feed written by writeFeed
// in the supplied format. If the file does not exist, 0 is returned with a nil error.
func getLatestID(p string, format feedFormat) (int64, error) {
	b, err := ioutil.ReadFile(p)
	if os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	var matches []string
	switch format {
	case atomFormat, rssFormat:
		matches = xmlLatestIDRegexp.FindStringSubmatch(string(b))
	case jsonFormat:
		var feed feeds.JSONFeed
		if err := json.Unmarshal(b, &feed); err != nil {
			return 0, errors.New("failed unmarshaling feed")
		}
		matches = jsonLatestIDRegexp.FindStringSubmatch(feed.UserComment)
	}
	if matches == nil {
		return 0, errors.New("couldn't find latest ID in comment")
	}
	return strconv.ParseInt(matches[1], 10, 64)
}

// debugParse reads an HTML timeline from p and dumps its tweets to stdout.
func debugParse(p string, replies bool) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	prof, tweets, err := parseTimeline(f)
	if err != nil {
		return err
	}

	fmt.Printf("%+v\n", prof)
	for _, t := range tweets {
		if replies || !t.reply() {
			fmt.Printf("%+v\n", t)
		}
	}
	return nil
}

// debug logs the supplied arguments using log.Print if verbose logging is enabled.
func debug(args ...interface{}) {
	if verbose {
		log.Print(args...)
	}
}

// debugf logs the supplied format string and args using log.Printf if verbose logging is enabled.
func debugf(format string, args ...interface{}) {
	if verbose {
		log.Printf(format, args...)
	}
}
