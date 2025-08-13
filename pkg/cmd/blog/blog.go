package blog

import (
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type RSS struct {
	XMLName xml.Name `xml:"rss"`
	Channel Channel  `xml:"channel"`
}

type Channel struct {
	XMLName xml.Name `xml:"channel"`
	Items   []Item   `xml:"item"`
}

type Item struct {
	Title       string `xml:"title"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	Link        string `xml:"link"`
}

func NewCmdBlog() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "blog",
		Short: "Blog-related commands",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	cmd.AddCommand(NewCmdBlogLatest())
	return cmd
}

func NewCmdBlogLatest() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "latest",
		Short: "Get the latest blog post from Depot's RSS feed",
		RunE:  runBlogLatest,
	}
	return cmd
}

func runBlogLatest(cmd *cobra.Command, args []string) error {
	resp, err := http.Get("https://depot.dev/rss.xml")
	if err != nil {
		return fmt.Errorf("failed to fetch RSS feed: %w", err)
	}
	defer resp.Body.Close()

	var rss RSS
	if err := xml.NewDecoder(resp.Body).Decode(&rss); err != nil {
		return fmt.Errorf("failed to parse RSS feed: %w", err)
	}

	if len(rss.Channel.Items) == 0 {
		return fmt.Errorf("no blog posts found in RSS feed")
	}

	item := rss.Channel.Items[0]

	pubDate, err := time.Parse("Mon, 02 Jan 2006 15:04:05 MST", item.PubDate)
	if err != nil {
		pubDate, err = time.Parse("Mon, 02 Jan 2006 15:04:05 -0700", item.PubDate)
		if err != nil {
			pubDate, err = time.Parse("Mon, 02 Jan 2006", item.PubDate)
			if err != nil {
				pubDate = time.Now()
			}
		}
	}

	fmt.Printf("Published: %s\n", pubDate.Format("January 2, 2006"))
	fmt.Printf("Link: %s\n\n", item.Link)
	fmt.Printf("# %s\n\n", html.UnescapeString(item.Title))

	description := html.UnescapeString(item.Description)
	description = stripHTML(description)
	description = wrapText(description, 80)

	fmt.Printf("%s\n\n", description)
	fmt.Printf("Read the full post at: %s\n", item.Link)

	return nil
}

func stripHTML(content string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	stripped := re.ReplaceAllString(content, "")
	stripped = strings.ReplaceAll(stripped, "&nbsp;", " ")
	stripped = strings.ReplaceAll(stripped, "&amp;", "&")
	stripped = strings.ReplaceAll(stripped, "&lt;", "<")
	stripped = strings.ReplaceAll(stripped, "&gt;", ">")
	stripped = strings.ReplaceAll(stripped, "&quot;", "\"")
	stripped = strings.ReplaceAll(stripped, "&#39;", "'")
	return strings.TrimSpace(stripped)
}

func wrapText(text string, width int) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return text
	}

	var lines []string
	var currentLine strings.Builder

	for _, word := range words {
		if currentLine.Len()+len(word)+1 > width && currentLine.Len() > 0 {
			lines = append(lines, currentLine.String())
			currentLine.Reset()
		}

		if currentLine.Len() > 0 {
			currentLine.WriteString(" ")
		}
		currentLine.WriteString(word)
	}

	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return strings.Join(lines, "\n")
}
