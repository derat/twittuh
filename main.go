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
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	baseFetchURL = "https://mobile.twitter.com/" // base timeline URL to fetch
	titleLen     = 80                            // max length of title text in feed
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flag]... <USER> <FILE>\n", os.Args[0])
		fmt.Fprintln(flag.CommandLine.Output(), "Creates an RSS feed from a Twitter user's timeline.\n")
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}
	cacheDir := flag.String("cache-dir", filepath.Join(os.Getenv("HOME"), ".cache/twittuh"), "Directory for caching downloads")
	debugFile := flag.String("debug-file", "", "HTML timeline file to parse for debugging")
	formatFlag := flag.String("format", "atom", `Feed format to write ("atom", "json", "rss")`)
	maxRequests := flag.Int("max-requests", 3, "Maximum number of HTTP requests to make to Twitter")
	replies := flag.Bool("replies", false, "Include the user's replies")
	flag.Parse()

	ft, err := newFetcher(*cacheDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed initializing fetcher: ", err)
		os.Exit(1)
	}

	if *debugFile != "" {
		if err := debug(ft, *debugFile, *replies); err != nil {
			fmt.Fprintln(os.Stderr, "Failed reading timeline: ", err)
			os.Exit(1)
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

	oldMaxID, err := getMaxID(feedPath, format)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't get previous max ID from %v: %v\n", feedPath, err)
	}
	tweets, err := getTweets(ft, user, oldMaxID, *maxRequests)
	if err == errUnchanged {
		os.Exit(0)
	} else if err == errPossibleGap {
		fmt.Println(os.Stderr, "Warning: possible gap in tweets (run more frequently or increase -max-requests)")
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "Failed getting tweets for %v: %v\n", user, err)
		os.Exit(1)
	}

	f, err := os.Create(feedPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed creating feed file: ", err)
		os.Exit(1)
	}
	if err := writeFeed(f, format, tweets, user, *replies); err != nil {
		f.Close()
		fmt.Fprintln(os.Stderr, "Failed writing feed: ", err)
		os.Exit(1)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "Failed closing feed file: ", err)
		os.Exit(1)
	}
}

var (
	errUnchanged   = errors.New("no new tweets")
	errPossibleGap = errors.New("possible gap in tweets")
)

// getTweets downloads and returns tweets from the supplied user's timeline.
// At most maxRequests will be issued to Twitter.
//
// If the ID of the latest tweet matches oldMaxID, errUnchanged is returned
// alongside an empty slice. This indicates that no new Tweets are present.
//
// If there is a possible gap between the tweets returned by the last invocation
// and the tweets returned by this invocation, errPossibleGap is returned
// alongside all the tweet that were fetched.
func getTweets(ft *fetcher, user string, oldMaxID int64, maxRequests int) ([]tweet, error) {
	var tweets []tweet
	baseURL := baseFetchURL + user
	url := baseURL
	for nr := 0; nr < maxRequests; nr++ {
		b, err := ft.fetch(url, false /* useCache */)
		if err != nil {
			return tweets, err
		}
		newTweets, err := parse(bytes.NewReader(b), ft)
		if err != nil {
			return tweets, err
		} else if len(newTweets) == 0 { // Went past the beginning of the feed?
			return tweets, nil
		}

		// Bail out early if there are no new tweets.
		if len(tweets) == 0 && newTweets[0].id == oldMaxID {
			return nil, errUnchanged
		}

		tweets = append(tweets, newTweets...)
		minID := newTweets[len(newTweets)-1].id
		url = fmt.Sprintf("%s?max_id=%v", baseURL, minID-1)
	}

	var err error
	if oldMaxID > 0 && tweets[len(tweets)-1].id > oldMaxID+1 {
		err = errPossibleGap
	}
	return tweets, err
}

// writeFeed writes a feed in the supplied format containing tweets from a user's timeline.
// If replies is true, the user's replies will also be included.
func writeFeed(w io.Writer, format feedFormat, tweets []tweet, user string, replies bool) error {
	// Try to find the user's name from one of the tweets.
	author := "@" + user
	for _, t := range tweets {
		if t.user == user {
			author = t.displayName()
			break
		}
	}

	feedDesc := "Tweets"
	if replies {
		feedDesc += " and replies"
	}
	feedDesc += fmt.Sprintf(" from @%v's timeline", user)

	feed := &feeds.Feed{
		Title:       author,
		Link:        &feeds.Link{Href: userURL(user)},
		Description: feedDesc,
		Author:      &feeds.Author{Name: author},
		Updated:     time.Now(),
		Copyright:   fmt.Sprintf("© %v %v", time.Now().Year(), author),
	}

	for _, t := range tweets {
		if !replies && t.reply() {
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
		if len(item.Title) > titleLen {
			item.Title = item.Title[:titleLen-1] + "…"
		}
		if t.imageURL != "" {
			item.Enclosure = &feeds.Enclosure{
				Url:  t.imageURL,
				Type: t.imageType,
			}
		}
		feed.Add(item)
	}

	var maxID int64
	if len(tweets) > 0 {
		maxID = tweets[0].id
	}

	switch format {
	case jsonFormat:
		// Embed the max ID in the feed's UserComment field.
		// The marshaling here matches feeds.Feed.WriteJSON().
		jf := (&feeds.JSON{Feed: feed}).JSONFeed()
		jf.UserComment = fmt.Sprintf("max id %v", maxID)
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
		// Embed the max ID in a trailing comment.
		_, err = fmt.Fprintf(w, "\n<!-- max id %v -->\n", maxID)
		return err
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// These match the comments added by writeFeed.
var xmlMaxIDRegexp = regexp.MustCompile(`<!--\s+max\s+id\s+(\d+)\s+-->\s*$`)
var jsonMaxIDRegexp = regexp.MustCompile(`^max id (\d+)$`)

// getMaxID attempts to find a maximum tweet ID embedded in p, a feed written by writeFeed
// in the supplied format. If the file does not exist, 0 is returned with a nil error.
func getMaxID(p string, format feedFormat) (int64, error) {
	b, err := ioutil.ReadFile(p)
	if os.IsNotExist(err) {
		return 0, nil
	} else if err != nil {
		return 0, err
	}

	var matches []string
	switch format {
	case atomFormat, rssFormat:
		matches = xmlMaxIDRegexp.FindStringSubmatch(string(b))
	case jsonFormat:
		var feed feeds.JSONFeed
		if err := json.Unmarshal(b, &feed); err != nil {
			return 0, errors.New("failed unmarshaling feed")
		}
		matches = jsonMaxIDRegexp.FindStringSubmatch(feed.UserComment)
	}
	if matches == nil {
		return 0, errors.New("couldn't find max ID in comment")
	}
	return strconv.ParseInt(matches[1], 10, 64)
}

// debug reads an HTML timeline from p and dumps its tweets to stdout.
func debug(ft *fetcher, p string, replies bool) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()

	tweets, err := parse(f, ft)
	if err != nil {
		return err
	}

	for _, t := range tweets {
		if replies || !t.reply() {
			fmt.Printf("%+v\n", t)
		}
	}
	return nil
}
