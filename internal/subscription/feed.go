package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
)

const omniHubNamespace = "https://github.com/ylxmf2005/omnihub/ns/1.0"

var ErrInvalidFeed = errors.New("invalid feed projection")

type FeedFormat string

const (
	FeedJSON FeedFormat = "json"
	FeedRSS  FeedFormat = "rss"
	FeedAtom FeedFormat = "atom"
)

type FeedRenderInput struct {
	View     core.View
	Snapshot SnapshotResult
	Format   FeedFormat
	FeedURL  string
	HomeURL  string
	Now      time.Time
}

type FeedResult struct {
	Body         []byte
	MediaType    string
	ETag         string
	LastModified time.Time
	Stale        bool
}

// NotModified 实现 GET/HEAD conditional precedence：有 If-None-Match 时忽略
// If-Modified-Since；ETag 使用 weak comparison，时间按 HTTP 秒精度比较。
func (result FeedResult) NotModified(ifNoneMatch, ifModifiedSince string) bool {
	if strings.TrimSpace(ifNoneMatch) != "" {
		for _, candidate := range strings.Split(ifNoneMatch, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || weakETag(candidate) == weakETag(result.ETag) {
				return true
			}
		}
		return false
	}
	modifiedSince, err := http.ParseTime(ifModifiedSince)
	return err == nil && !result.LastModified.Truncate(time.Second).After(modifiedSince)
}

// RenderFeed 只读取已经提交的 Snapshot。它不会持有 Service、Store 或
// Adapter，因此任何调用路径都不可能在序列化 Feed 时隐式访问上游。
func RenderFeed(input FeedRenderInput) (FeedResult, error) {
	if input.View.ID == "" || input.Snapshot.Snapshot.ViewID != input.View.ID {
		return FeedResult{}, fmt.Errorf("%w: view and snapshot do not match", ErrInvalidFeed)
	}
	envelope, err := decodeSnapshot(input.Snapshot.Snapshot)
	if err != nil {
		return FeedResult{}, fmt.Errorf("%w: %v", ErrInvalidFeed, err)
	}
	now := input.Now.UTC()
	if input.Now.IsZero() {
		now = time.Now().UTC()
	}
	stale := !input.Snapshot.Snapshot.FreshUntil.After(now)
	metadata := feedMetadata{
		ViewID: input.View.ID, RunID: input.Snapshot.Snapshot.RunID,
		SnapshotAt: input.Snapshot.Snapshot.CreatedAt.UTC().Format(time.RFC3339Nano),
		FreshUntil: input.Snapshot.Snapshot.FreshUntil.UTC().Format(time.RFC3339Nano), Stale: stale,
	}

	var body []byte
	var mediaType string
	switch input.Format {
	case FeedJSON:
		body, err = json.Marshal(projectJSONFeed(input, envelope, metadata))
		mediaType = "application/feed+json; charset=utf-8"
	case FeedRSS:
		body, err = marshalXML(projectRSS(input, envelope, metadata))
		mediaType = "application/rss+xml; charset=utf-8"
	case FeedAtom:
		body, err = marshalXML(projectAtom(input, envelope, metadata))
		mediaType = "application/atom+xml; charset=utf-8"
	default:
		return FeedResult{}, fmt.Errorf("%w: unsupported format %q", ErrInvalidFeed, input.Format)
	}
	if err != nil {
		return FeedResult{}, fmt.Errorf("%w: encode %s: %v", ErrInvalidFeed, input.Format, err)
	}
	digest := sha256.Sum256(body)
	lastModified := input.Snapshot.Snapshot.CreatedAt.UTC()
	if stale && input.Snapshot.Snapshot.FreshUntil.After(lastModified) {
		// stale 是 representation 的一部分；若它在 Snapshot 写入后翻转，
		// Last-Modified 也必须跨过该边界，不能让 IMS 错误返回 304。
		lastModified = input.Snapshot.Snapshot.FreshUntil.UTC()
	}
	return FeedResult{
		Body: body, MediaType: mediaType, ETag: `"` + hex.EncodeToString(digest[:]) + `"`,
		LastModified: lastModified, Stale: stale,
	}, nil
}

type feedMetadata struct {
	ViewID     string `json:"view_id" xml:"viewId,attr"`
	RunID      string `json:"run_id" xml:"runId,attr"`
	SnapshotAt string `json:"snapshot_at" xml:"snapshotAt,attr"`
	FreshUntil string `json:"fresh_until" xml:"freshUntil,attr"`
	Stale      bool   `json:"stale" xml:"stale,attr"`
}

type feedOrigin struct {
	Source          string `json:"source" xml:"source,attr"`
	Provider        string `json:"provider" xml:"provider,attr"`
	ChannelID       string `json:"channel_id" xml:"channelId,attr"`
	RouteTemplateID string `json:"route_template_id" xml:"routeTemplateId,attr"`
	OriginalURL     string `json:"original_url" xml:",chardata"`
	CanonicalURL    string `json:"canonical_url,omitempty" xml:"canonicalUrl,attr,omitempty"`
	RetrievedAt     string `json:"retrieved_at" xml:"retrievedAt,attr"`
	Verification    string `json:"verification" xml:"verification,attr"`
}

type itemMetadata struct {
	Identity string       `json:"identity" xml:"identity,attr"`
	Role     string       `json:"content_role" xml:"contentRole,attr"`
	Origins  []feedOrigin `json:"origins" xml:"omnihub:origin"`
}

func itemMeta(item core.Item) itemMetadata {
	origins := make([]feedOrigin, 0, len(item.Observations))
	for _, observation := range item.Observations {
		origins = append(origins, feedOrigin{
			Source: observation.Source, Provider: observation.Provider, ChannelID: observation.ChannelID,
			RouteTemplateID: observation.RouteTemplateID, OriginalURL: observation.OriginalURL,
			CanonicalURL: observation.CanonicalURL, RetrievedAt: observation.RetrievedAt.UTC().Format(time.RFC3339Nano),
			Verification: string(observation.Verification),
		})
	}
	return itemMetadata{Identity: item.Identity.ClusterID, Role: string(item.Content.Role), Origins: origins}
}

type jsonFeed struct {
	Version     string         `json:"version"`
	Title       string         `json:"title"`
	HomePageURL string         `json:"home_page_url,omitempty"`
	FeedURL     string         `json:"feed_url,omitempty"`
	Items       []jsonFeedItem `json:"items"`
	OmniHub     feedMetadata   `json:"_omnihub"`
}

type jsonFeedItem struct {
	ID            string           `json:"id"`
	URL           string           `json:"url,omitempty"`
	ExternalURL   *string          `json:"external_url,omitempty"`
	Title         string           `json:"title,omitempty"`
	ContentText   *string          `json:"content_text,omitempty"`
	ContentHTML   *string          `json:"content_html,omitempty"`
	Summary       *string          `json:"summary,omitempty"`
	Image         *string          `json:"image,omitempty"`
	BannerImage   *string          `json:"banner_image,omitempty"`
	DatePublished *time.Time       `json:"date_published,omitempty"`
	DateModified  *time.Time       `json:"date_modified,omitempty"`
	Authors       []jsonFeedAuthor `json:"authors,omitempty"`
	Tags          []string         `json:"tags,omitempty"`
	Attachments   []jsonAttachment `json:"attachments,omitempty"`
	OmniHub       itemMetadata     `json:"_omnihub"`
}

type jsonFeedAuthor struct {
	Name   string  `json:"name"`
	URL    *string `json:"url,omitempty"`
	Avatar *string `json:"avatar,omitempty"`
}

type jsonAttachment struct {
	URL      string  `json:"url"`
	MIMEType *string `json:"mime_type,omitempty"`
	Title    *string `json:"title,omitempty"`
}

func projectJSONFeed(input FeedRenderInput, envelope core.Envelope, metadata feedMetadata) jsonFeed {
	items := make([]jsonFeedItem, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		authors := make([]jsonFeedAuthor, 0, len(item.Authors))
		for _, author := range item.Authors {
			authors = append(authors, jsonFeedAuthor{Name: author.Name, URL: author.URL, Avatar: author.Avatar})
		}
		attachments := make([]jsonAttachment, 0, len(item.Attachments))
		for _, attachment := range item.Attachments {
			attachments = append(attachments, jsonAttachment{URL: attachment.URL, MIMEType: attachment.MIMEType, Title: attachment.Title})
		}
		items = append(items, jsonFeedItem{
			ID: item.ID, URL: item.URL, ExternalURL: item.ExternalURL, Title: item.Title,
			ContentText: item.Content.Text, ContentHTML: item.Content.HTML, Summary: item.Summary,
			Image: item.Image, BannerImage: item.BannerImage, DatePublished: item.PublishedAt,
			DateModified: item.ModifiedAt, Authors: authors, Tags: item.Tags, Attachments: attachments,
			OmniHub: itemMeta(item),
		})
	}
	return jsonFeed{
		Version: "https://jsonfeed.org/version/1.1", Title: input.View.DisplayName,
		HomePageURL: input.HomeURL, FeedURL: input.FeedURL, Items: items, OmniHub: metadata,
	}
}

type rssDocument struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	XMLNS   string     `xml:"xmlns:omnihub,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string       `xml:"title"`
	Link          string       `xml:"link,omitempty"`
	Description   string       `xml:"description"`
	LastBuildDate string       `xml:"lastBuildDate"`
	Metadata      feedMetadata `xml:"omnihub:metadata"`
	Items         []rssItem    `xml:"item"`
}

type rssItem struct {
	Title       string       `xml:"title,omitempty"`
	Link        string       `xml:"link,omitempty"`
	GUID        rssGUID      `xml:"guid"`
	Description string       `xml:"description,omitempty"`
	Published   string       `xml:"pubDate,omitempty"`
	Modified    string       `xml:"omnihub:modifiedAt,omitempty"`
	Categories  []string     `xml:"category,omitempty"`
	Authors     []xmlAuthor  `xml:"omnihub:author,omitempty"`
	Content     xmlContent   `xml:"omnihub:content"`
	Metadata    itemMetadata `xml:"omnihub:metadata"`
}

type rssGUID struct {
	PermaLink bool   `xml:"isPermaLink,attr"`
	Value     string `xml:",chardata"`
}

type xmlAuthor struct {
	Name   string `xml:"name,attr"`
	URL    string `xml:"url,attr,omitempty"`
	Avatar string `xml:"avatar,attr,omitempty"`
}

type xmlContent struct {
	Role   string `xml:"role,attr"`
	Format string `xml:"format,attr"`
	Value  string `xml:",chardata"`
}

func projectRSS(input FeedRenderInput, envelope core.Envelope, metadata feedMetadata) rssDocument {
	items := make([]rssItem, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		authors := make([]xmlAuthor, 0, len(item.Authors))
		for _, author := range item.Authors {
			authors = append(authors, xmlAuthor{Name: author.Name, URL: pointerValue(author.URL), Avatar: pointerValue(author.Avatar)})
		}
		content, format := contentValue(item.Content)
		items = append(items, rssItem{
			Title: item.Title, Link: item.URL, GUID: rssGUID{Value: item.ID},
			Description: descriptionValue(item), Published: rssTime(item.PublishedAt), Modified: timeValueRFC3339(item.ModifiedAt),
			Categories: item.Tags, Authors: authors, Content: xmlContent{Role: string(item.Content.Role), Format: format, Value: content},
			Metadata: itemMeta(item),
		})
	}
	link := input.HomeURL
	if link == "" {
		link = input.FeedURL
	}
	return rssDocument{
		Version: "2.0", XMLNS: omniHubNamespace,
		Channel: rssChannel{
			Title: input.View.DisplayName, Link: link, Description: "OmniHub View " + input.View.DisplayName,
			LastBuildDate: input.Snapshot.Snapshot.CreatedAt.UTC().Format(time.RFC1123Z), Metadata: metadata, Items: items,
		},
	}
}

type atomDocument struct {
	XMLName  xml.Name     `xml:"feed"`
	XMLNS    string       `xml:"xmlns,attr"`
	OmniNS   string       `xml:"xmlns:omnihub,attr"`
	Title    string       `xml:"title"`
	ID       string       `xml:"id"`
	Updated  string       `xml:"updated"`
	Links    []atomLink   `xml:"link,omitempty"`
	Metadata feedMetadata `xml:"omnihub:metadata"`
	Entries  []atomEntry  `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	Title      string         `xml:"title"`
	ID         string         `xml:"id"`
	Links      []atomLink     `xml:"link,omitempty"`
	Published  string         `xml:"published,omitempty"`
	Updated    string         `xml:"updated"`
	Summary    string         `xml:"summary,omitempty"`
	Content    atomContent    `xml:"content"`
	Authors    []atomAuthor   `xml:"author,omitempty"`
	Categories []atomCategory `xml:"category,omitempty"`
	Metadata   itemMetadata   `xml:"omnihub:metadata"`
}

type atomContent struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type atomAuthor struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

func projectAtom(input FeedRenderInput, envelope core.Envelope, metadata feedMetadata) atomDocument {
	entries := make([]atomEntry, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		authors := make([]atomAuthor, 0, len(item.Authors))
		for _, author := range item.Authors {
			authors = append(authors, atomAuthor{Name: author.Name, URI: pointerValue(author.URL)})
		}
		categories := make([]atomCategory, 0, len(item.Tags))
		for _, tag := range item.Tags {
			categories = append(categories, atomCategory{Term: tag})
		}
		content, format := contentValue(item.Content)
		updated := input.Snapshot.Snapshot.CreatedAt.UTC()
		if item.PublishedAt != nil {
			updated = item.PublishedAt.UTC()
		}
		if item.ModifiedAt != nil {
			updated = item.ModifiedAt.UTC()
		}
		links := make([]atomLink, 0, 2)
		if item.URL != "" {
			links = append(links, atomLink{Rel: "alternate", Href: item.URL})
		}
		if item.ExternalURL != nil {
			links = append(links, atomLink{Rel: "related", Href: *item.ExternalURL})
		}
		entries = append(entries, atomEntry{
			Title: item.Title, ID: atomItemID(item), Links: links,
			Published: timeValueRFC3339(item.PublishedAt), Updated: updated.Format(time.RFC3339Nano),
			Summary: pointerValue(item.Summary), Content: atomContent{Type: format, Value: content},
			Authors: authors, Categories: categories, Metadata: itemMeta(item),
		})
	}
	links := make([]atomLink, 0, 2)
	if input.FeedURL != "" {
		links = append(links, atomLink{Rel: "self", Href: input.FeedURL, Type: "application/atom+xml"})
	}
	if input.HomeURL != "" {
		links = append(links, atomLink{Rel: "alternate", Href: input.HomeURL})
	}
	feedID := input.FeedURL
	if feedID == "" {
		feedID = "urn:omnihub:view:" + input.View.ID
	}
	return atomDocument{
		XMLNS: "http://www.w3.org/2005/Atom", OmniNS: omniHubNamespace,
		Title: input.View.DisplayName, ID: feedID, Updated: input.Snapshot.Snapshot.CreatedAt.UTC().Format(time.RFC3339Nano),
		Links: links, Metadata: metadata, Entries: entries,
	}
}

func marshalXML(value any) ([]byte, error) {
	encoded, err := xml.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), encoded...), nil
}

func descriptionValue(item core.Item) string {
	if item.Summary != nil {
		return *item.Summary
	}
	value, _ := contentValue(item.Content)
	return value
}

func contentValue(content core.Content) (string, string) {
	if content.HTML != nil {
		return *content.HTML, "html"
	}
	if content.Text != nil {
		return *content.Text, "text"
	}
	return "", "text"
}

func atomItemID(item core.Item) string {
	if strings.Contains(item.ID, ":") {
		return item.ID
	}
	return "urn:omnihub:item:" + item.ID
}

func rssTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC1123Z)
}

func timeValueRFC3339(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func weakETag(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "W/") || strings.HasPrefix(value, "w/") {
		value = strings.TrimSpace(value[2:])
	}
	return value
}
