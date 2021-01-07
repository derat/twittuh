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
	titleLen                = 80   // max length of title text in feed, in runes
	defaultMode os.FileMode = 0644 // default mode for new feed files
)

var verbose = false // enable verbose logging

func main() {
	var fetchOpts fetchOptions
	var parseOpts parseOptions

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <user> <file>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	browserSize := flag.String("browser-size", "1024x8192", "Browser viewport size")
	flag.StringVar(&fetchOpts.cacheDir, "cache-dir", "", "Chrome cache directory")
	flag.BoolVar(&fetchOpts.logDebug, "debug-chrome", false, "Log noisy Chrome debug messages")
	debugFile := flag.String("debug-file", "", "HTML timeline file to parse for debugging")
	dumpDOM := flag.Bool("dump-dom", false, "Dump the timeline DOM to stdout for debugging")
	fetchRetries := flag.Int("fetch-retries", 0, "Number of times to retry fetching")
	fetchTimeout := flag.Int("fetch-timeout", 0, "Fetch timeout in seconds")
	force := flag.Bool("force", false, "Write feed even if there are no new tweets")
	formatFlag := flag.String("format", "atom", `Feed format to write ("atom", "json", "rss")`)
	flag.StringVar(&fetchOpts.proxy, "proxy", "", `Optional proxy server (e.g. "socks5://localhost:9050")`)
	pageSettleDelay := flag.Int("page-settle-delay", 2, "Time to wait for page render in seconds")
	replies := flag.Bool("replies", false, "Include the user's replies")
	skipUsers := flag.String("skip-users", "", "Comma-separated users whose tweets should be skipped")
	flag.BoolVar(&parseOpts.simplify, "simplify", true, "Simplify HTML in feed")
	tweetTimeout := flag.Int("tweet-timeout", 0, "Timeout for loading tweets in seconds")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")
	flag.Parse()

	ctx := context.Background()

	if *debugFile != "" {
		if err := debugParse(*debugFile, parseOpts, *replies); err != nil {
			log.Fatal("Failed reading timeline: ", err)
		}
		os.Exit(0)
	}

	if len(flag.Args()) != 2 && !*dumpDOM {
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
	var werr, herr error
	fetchOpts.width, werr = strconv.Atoi(ps[0])
	fetchOpts.height, herr = strconv.Atoi(ps[1])
	if werr != nil || herr != nil {
		log.Fatalf("Bad browser size %q", *browserSize)
	}

	fetchOpts.pageSettleDelay = time.Duration(*pageSettleDelay) * time.Second
	fetchOpts.tweetTimeout = time.Duration(*tweetTimeout) * time.Second

	var oldLatestID int64
	var err error
	if !*force {
		if oldLatestID, err = getLatestID(feedPath, format); err != nil {
			log.Printf("Couldn't get old latest ID from %v: %v", feedPath, err)
		}
	}

	debugf("Getting timeline for %v with old latest ID %v", user, oldLatestID)
	var dom string
	var attempts int
	for {
		if *fetchTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(*fetchTimeout)*time.Second)
			defer cancel()
		}
		attempts++
		if dom, err = fetchTimeline(ctx, user, fetchOpts); err == nil {
			break
		} else {
			if attempts > *fetchRetries {
				log.Fatalf("Failed fetching timeline for %v: %v", user, err)
			} else {
				debugf("Fetching timeline failed; trying again: %v", err)
			}
		}
	}
	if *dumpDOM {
		os.Stdout.WriteString(dom)
		os.Exit(0)
	}

	prof, tweets, err := parseTimeline(strings.NewReader(dom), parseOpts)
	if err != nil {
		log.Fatalf("Failed parsing timeline for %v: %v", user, err)
	} else if len(tweets) == 0 {
		log.Fatalf("No tweets found for %v", user)
	}
	debugf("Parsed %v tweet(s)", len(tweets))

	var latestID int64
	for _, tw := range tweets {
		if tw.ID > latestID {
			latestID = tw.ID
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

// writeFeed writes a feed in the supplied format containing tweets from a user's timeline.
// If replies is true, the user's replies will also be included.
func writeFeed(w io.Writer, format feedFormat, prof profile, tweets []tweet,
	latestID int64, replies bool, skipUsers []string) error {
	author := prof.displayName()
	feedDesc := "Tweets"
	if replies {
		feedDesc += " and replies"
	}
	feedDesc += fmt.Sprintf(" from @%v's timeline", prof.User)

	feed := &feeds.Feed{
		Title:       author,
		Link:        &feeds.Link{Href: userURL(prof.User)},
		Description: feedDesc,
		Author:      &feeds.Author{Name: author},
		Updated:     time.Now(),
		Copyright:   fmt.Sprintf("© %v %v", time.Now().Year(), author),
	}
	if prof.Image != "" {
		feed.Image = &feeds.Image{Url: prof.Image}
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
		if _, ok := skipUsersMap[strings.ToLower(t.User)]; ok && t.User != prof.User {
			continue
		}

		item := &feeds.Item{
			Title:       t.Text,
			Link:        &feeds.Link{Href: t.Href}, // Atom's default rel is "alternate"
			Description: t.Text,
			Author:      &feeds.Author{Name: t.displayName()},
			Id:          fmt.Sprintf("%v", t.ID),
			Created:     t.Time,
			Updated:     t.Time,
			Content:     t.Content,
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
		jf.Favicon = prof.Icon
		jf.Icon = prof.Image
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
func debugParse(p string, opts parseOptions, replies bool) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	prof, tweets, err := parseTimeline(f, opts)
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
