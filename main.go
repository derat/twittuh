// Copyright 2020 Daniel Erat. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
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
	cacheDir := flag.String("cache-dir", filepath.Join(os.Getenv("HOME"), ".cache/twittuh"), "Directory for caching downloads")
	debugFile := flag.String("debug-file", "", "HTML timeline file to parse for debugging")
	embeds := flag.Bool("embeds", true, "Rewrite tweets to include embedded images and tweets")
	force := flag.Bool("force", false, "Download and write feed even if there are no new tweets")
	formatFlag := flag.String("format", "atom", `Feed format to write ("atom", "json", "rss")`)
	pages := flag.Int("pages", 3, "Timeline pages to request (20 tweets/replies per page)")
	replies := flag.Bool("replies", false, "Include the user's replies")
	skipUsers := flag.String("skip-users", "", "Comma-separated users whose tweets should be skipped")
	userAgent := flag.String("user-agent", "", "User-Agent header to include in HTTP requests")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")
	flag.Parse()

	ft, err := newFetcher(*cacheDir)
	if err != nil {
		log.Fatal("Failed initializing fetcher: ", err)
	}
	if *userAgent != "" {
		ft.userAgent = *userAgent
	}

	if *debugFile != "" {
		if err := debugParse(ft, *debugFile, *replies, *embeds); err != nil {
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

	var oldLatestID int64
	if !*force {
		if oldLatestID, err = getLatestID(feedPath, format); err != nil {
			log.Printf("Couldn't get old latest ID from %v: %v", feedPath, err)
		}
	}

	debugf("Getting timeline for %v with old latest ID %v", user, oldLatestID)
	prof, tweets, latestID, err := getTimeline(ft, user, oldLatestID, *pages, *embeds, false /* cache */)
	if err == errUnchanged {
		debug("No new tweets; exiting without writing feed")
		os.Exit(0)
	} else if err != nil {
		log.Fatalf("Failed getting tweets for %v: %v", user, err)
	} else if len(tweets) == 0 {
		log.Fatalf("No tweets found for %v", user)
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

var (
	errUnchanged = errors.New("no new tweets")
)

// getTweets downloads and returns tweets from the supplied user's timeline.
// The specified number of pages are downloaded. Each page appears to include
// 20 tweets (or replies).
//
// If the ID of the latest tweet matches oldLatestID, errUnchanged is returned
// alongside an empty slice. This indicates that no new Tweets are present.
func getTimeline(ft *fetcher, user string, oldLatestID int64, pages int,
	embeds, cache bool) (prof profile, tweets []tweet, latestID int64, err error) {
	seenIDs := make(map[int64]struct{})
	baseURL := mobileURL(userURL(user))
	url := baseURL
	for np := 0; np < pages; np++ {
		b, err := ft.fetch(url, cache)
		if err != nil {
			return prof, tweets, 0, err
		}
		var newTweets []tweet
		var nextURL string
		if prof, newTweets, nextURL, err = parse(bytes.NewReader(b), ft, embeds); err != nil {
			return prof, tweets, latestID, err
		} else if len(newTweets) == 0 { // Went past the beginning of the feed?
			return prof, tweets, latestID, nil
		}

		if np == 0 {
			latestID = newTweets[0].id
			// Bail out early if there are no new tweets.
			if latestID == oldLatestID {
				return prof, tweets, latestID, errUnchanged
			}
		}

		for _, t := range newTweets {
			// Because of the way that retweets are mixed in, we might get overlapping ranges of
			// tweets in subsequent requests.
			if _, ok := seenIDs[t.id]; !ok {
				tweets = append(tweets, t)
				seenIDs[t.id] = struct{}{}
			}
		}

		if nextURL == "" {
			break // reached the beginning of the timeline?
		}
		url = nextURL
	}

	debugf("Parsed %v tweet(s)", len(tweets))
	return prof, tweets, latestID, nil
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
func debugParse(ft *fetcher, p string, replies, embeds bool) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	prof, tweets, _, err := parse(f, ft, embeds)
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
